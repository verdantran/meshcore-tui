package repeaterserial

import (
	"context"
	"io"
	"sync"

	"go.bug.st/serial"

	"meshcore-tui/internal/radio"
)

// USB is whatever answers on a serial port: a companion radio if the handshake works, otherwise a
// repeater's own console. The companion goes first so the usual case isn't slowed by probing.
type USB struct {
	port         string
	newCompanion func() radio.Radio
	open         func() (io.ReadWriteCloser, error)
	events       chan radio.Event

	mu    sync.Mutex
	inner radio.Radio
}

func NewUSB(port string) *USB {
	return &USB{port: port, events: make(chan radio.Event, 256),
		newCompanion: func() radio.Radio { return radio.NewSerial(port) },
		open:         func() (io.ReadWriteCloser, error) { return openPort(port) }}
}

func openPort(port string) (io.ReadWriteCloser, error) {
	return serial.Open(port, &serial.Mode{BaudRate: 115200})
}

func (u *USB) cur() radio.Radio {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inner
}

func (u *USB) set(r radio.Radio) {
	u.mu.Lock()
	u.inner = r
	u.mu.Unlock()
}

func (u *USB) Events() <-chan radio.Event { return u.events }
func (u *USB) Transport() string          { return "USB" }

func (u *USB) Describe() string {
	if r := u.cur(); r != nil {
		return r.Describe()
	}
	return u.port
}

func (u *USB) Close() error {
	if r := u.cur(); r != nil {
		return r.Close()
	}
	return nil
}

func (u *USB) Start(ctx context.Context) {
	comp := u.newCompanion()
	u.set(comp)
	comp.Start(ctx)
	go u.watch(ctx, comp)
}

// watch passes the companion's events on. A failed step is held until the next event shows whether
// the connect gave up; if it did, the port is probed for a repeater console before reporting failure.
func (u *USB) watch(ctx context.Context, comp radio.Radio) {
	var held *radio.BootStep
	for {
		var e radio.Event
		select {
		case e = <-comp.Events():
		case <-ctx.Done():
			return
		}
		if step, ok := e.(radio.BootStep); ok && step.State == radio.StepFail {
			if held != nil {
				u.events <- *held
			}
			held = &step
			continue
		}
		if f, ok := e.(radio.Fatal); ok {
			comp.Close()
			if rep := u.probe(ctx); rep != nil {
				u.events <- radio.BootStep{Key: "hello", Label: "HELLO", Detail: "no companion here; found a repeater console", State: radio.StepOK}
				u.set(rep)
				rep.Start(ctx)
				u.forward(ctx, rep)
				return
			}
			if held != nil {
				u.events <- *held
			}
			u.events <- f
			continue
		}
		if held != nil {
			u.events <- *held
			held = nil
		}
		u.events <- e
	}
}

func (u *USB) probe(ctx context.Context) *Repeater {
	rw, err := u.open()
	if err != nil {
		return nil
	}
	con := NewConsole(rw)
	if _, ok := Probe(ctx, con); !ok {
		con.Close()
		return nil
	}
	return NewRepeater(u.port, con, u.open)
}

func (u *USB) forward(ctx context.Context, r radio.Radio) {
	for {
		select {
		case e := <-r.Events():
			u.events <- e
		case <-ctx.Done():
			return
		}
	}
}

var (
	_ radio.Radio = (*USB)(nil)
	_ radio.Radio = (*Repeater)(nil)
)

func (u *USB) SendText(ctx context.Context, to [32]byte, text string) (radio.Sent, error) {
	return u.cur().SendText(ctx, to, text)
}
func (u *USB) SendChannel(ctx context.Context, idx int, text string) error {
	return u.cur().SendChannel(ctx, idx, text)
}
func (u *USB) ResetPath(ctx context.Context, k [32]byte) error { return u.cur().ResetPath(ctx, k) }
func (u *USB) SyncContacts(ctx context.Context) ([]radio.Contact, error) {
	return u.cur().SyncContacts(ctx)
}
func (u *USB) SaveContact(ctx context.Context, c radio.Contact) error {
	return u.cur().SaveContact(ctx, c)
}
func (u *USB) RemoveContact(ctx context.Context, k [32]byte) error {
	return u.cur().RemoveContact(ctx, k)
}
func (u *USB) Trace(ctx context.Context, path []byte) (uint32, error) {
	return u.cur().Trace(ctx, path)
}
func (u *USB) DiscoverPath(ctx context.Context, k [32]byte) error {
	return u.cur().DiscoverPath(ctx, k)
}
func (u *USB) AdvertPath(ctx context.Context, k [32]byte) (radio.AdvertPath, error) {
	return u.cur().AdvertPath(ctx, k)
}
func (u *USB) ExportContact(ctx context.Context, k [32]byte) (string, error) {
	return u.cur().ExportContact(ctx, k)
}
func (u *USB) ImportContact(ctx context.Context, link string) error {
	return u.cur().ImportContact(ctx, link)
}
func (u *USB) AutoAdd(ctx context.Context) (radio.AutoAdd, error) { return u.cur().AutoAdd(ctx) }
func (u *USB) SetAutoAdd(ctx context.Context, a radio.AutoAdd) error {
	return u.cur().SetAutoAdd(ctx, a)
}
func (u *USB) Login(ctx context.Context, k [32]byte, password string) error {
	return u.cur().Login(ctx, k, password)
}
func (u *USB) Logout(ctx context.Context, k [32]byte) error { return u.cur().Logout(ctx, k) }
func (u *USB) RequestStatus(ctx context.Context, k [32]byte) error {
	return u.cur().RequestStatus(ctx, k)
}
func (u *USB) RequestTelemetry(ctx context.Context, k [32]byte) error {
	return u.cur().RequestTelemetry(ctx, k)
}
func (u *USB) BinaryRequest(ctx context.Context, k [32]byte, data []byte) (uint32, error) {
	return u.cur().BinaryRequest(ctx, k, data)
}
func (u *USB) SendCLI(ctx context.Context, k [32]byte, command string) error {
	return u.cur().SendCLI(ctx, k, command)
}

func (u *USB) Device() radio.Device { return u.cur().Device() }

func (u *USB) Channels(ctx context.Context) ([]radio.Channel, error) { return u.cur().Channels(ctx) }
func (u *USB) SetChannel(ctx context.Context, c radio.Channel) error {
	return u.cur().SetChannel(ctx, c)
}
