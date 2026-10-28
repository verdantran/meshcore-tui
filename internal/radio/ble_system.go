//go:build darwin || linux || windows

package radio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OwlShack/meshcore-go/companion"
	"tinygo.org/x/bluetooth"

	"github.com/verdantran/meshcore-tui/internal/untrusted"
)

const (
	bleConnectTimeout = 10 * time.Second
	bleFindTimeout    = 8 * time.Second // looking for a radio the system doesn't know yet
)

var (
	bleService = mustUUID(companion.BLEServiceUUID)
	bleRx      = mustUUID(companion.BLECharacteristicRxUUID) // we write commands here
	bleTx      = mustUUID(companion.BLECharacteristicTxUUID) // the radio notifies responses here
)

func mustUUID(s string) bluetooth.UUID {
	u, err := bluetooth.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

// systemAdapter is the computer's Bluetooth, which can only be enabled once per process.
type systemAdapter struct {
	enabled sync.Once
	err     error
	a       *bluetooth.Adapter
	scanMu  sync.Mutex

	mu    sync.Mutex
	peers map[string]*systemPeer // by upper-case address
}

var theAdapter = &systemAdapter{peers: map[string]*systemPeer{}}

func systemBLE() bleAdapter { return theAdapter }

func (s *systemAdapter) enable() error {
	s.enabled.Do(func() {
		s.a = bluetooth.DefaultAdapter
		if s.err = s.a.Enable(); s.err != nil {
			return
		}
		s.a.SetConnectHandler(func(d bluetooth.Device, connected bool) {
			if connected {
				return
			}
			s.mu.Lock()
			p := s.peers[strings.ToUpper(d.Address.String())]
			s.mu.Unlock()
			if p != nil {
				p.lost()
			}
		})
	})
	return s.err
}

func (s *systemAdapter) connect(ctx context.Context, addr string, notify func([]byte), dropped func()) (blePeer, error) {
	if err := s.enable(); err != nil {
		return nil, err
	}
	address, err := parseBLEAddress(addr)
	if err != nil {
		return nil, fmt.Errorf("%q isn't a Bluetooth address: %w", addr, err)
	}
	params := bluetooth.ConnectionParams{ConnectionTimeout: bluetooth.NewDuration(bleConnectTimeout)}
	dev, err := s.a.Connect(address, params)
	if err != nil {
		// The system may not know the radio yet, as after a restart: find it, then try again.
		if found, ok := s.find(ctx, addr); ok {
			dev, err = s.a.Connect(found, params)
		}
	}
	if err != nil {
		return nil, err
	}
	key := strings.ToUpper(addr)
	p := &systemPeer{dev: dev, dropped: dropped, forget: func() { s.mu.Lock(); delete(s.peers, key); s.mu.Unlock() }}
	s.mu.Lock()
	s.peers[key] = p
	s.mu.Unlock()
	if err := p.open(notify); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}

// scan reports radios offering the companion service, or named as MeshCore radios are, until ctx ends.
func (s *systemAdapter) scan(ctx context.Context, found func(BLEDevice)) error {
	if err := s.enable(); err != nil {
		return err
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		// The scan may not have started yet when ctx ends, so keep asking until it stops.
		for s.a.StopScan() != nil {
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	return s.a.Scan(func(_ *bluetooth.Adapter, r bluetooth.ScanResult) {
		name := r.LocalName()
		if r.HasServiceUUID(bleService) || strings.HasPrefix(name, "MeshCore") {
			found(BLEDevice{Addr: r.Address.String(), Name: untrusted.Line(name), RSSI: r.RSSI})
		}
	})
}

// find scans for one radio by its address.
func (s *systemAdapter) find(ctx context.Context, addr string) (bluetooth.Address, bool) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, bleFindTimeout)
	defer cancel()
	stopped := make(chan struct{})
	defer func() { <-stopped }() // so the timeout can't stop a later scan
	go func() {
		<-ctx.Done()
		s.a.StopScan()
		close(stopped)
	}()
	hit := make(chan bluetooth.Address, 1)
	s.a.Scan(func(a *bluetooth.Adapter, r bluetooth.ScanResult) {
		if strings.EqualFold(r.Address.String(), addr) {
			select {
			case hit <- r.Address:
				a.StopScan()
			default:
			}
		}
	})
	cancel()
	select {
	case h := <-hit:
		return h, true
	default:
		return bluetooth.Address{}, false
	}
}

type systemPeer struct {
	dev     bluetooth.Device
	rx, tx  bluetooth.DeviceCharacteristic
	dropped func()
	forget  func()
	closed  sync.Once
}

func (p *systemPeer) open(notify func([]byte)) error {
	svcs, err := p.dev.DiscoverServices([]bluetooth.UUID{bleService})
	if err != nil || len(svcs) == 0 {
		return fmt.Errorf("no MeshCore companion service; is the radio running the Bluetooth companion firmware? %v", err)
	}
	chars, err := svcs[0].DiscoverCharacteristics([]bluetooth.UUID{bleRx, bleTx})
	if err != nil {
		return err
	}
	var haveRx, haveTx bool
	for _, c := range chars {
		switch c.UUID() {
		case bleRx:
			p.rx, haveRx = c, true
		case bleTx:
			p.tx, haveTx = c, true
		}
	}
	if !haveRx || !haveTx {
		return errors.New("the radio's companion service is missing a characteristic")
	}
	return p.tx.EnableNotifications(notify)
}

func (p *systemPeer) write(frame []byte) error {
	if most, ok := bleRoomFor(len(frame), p.carries, time.Sleep); !ok {
		return fmt.Errorf("a %d-byte frame is more than this Bluetooth link carries in one write (%d)", len(frame), most)
	}
	_, err := p.rx.Write(frame)
	return err
}

// carries is the most one write takes now, or 0 if the link doesn't say. It's asked each time
// because the MTU grows after connecting: Windows starts at the minimum and agrees more later.
func (p *systemPeer) carries() int {
	if mtu, err := p.rx.GetMTU(); err == nil && mtu > 3 {
		return int(mtu) - 3 // less the ATT header
	}
	return 0
}

// lost is the system saying the radio went. Windows has already disconnected and released the
// device by then, so disconnecting it again would free it twice and crash the app.
func (p *systemPeer) lost() {
	if bleOS == "windows" {
		p.closed.Do(p.forget)
	}
	p.dropped()
}

func (p *systemPeer) close() (err error) {
	p.closed.Do(func() {
		p.forget()
		err = p.dev.Disconnect()
	})
	return err
}
