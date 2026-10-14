package sim

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"sync"

	meshcore "github.com/OwlShack/meshcore-go"

	"meshcore-tui/internal/radio"
)

var (
	idMu sync.Mutex
	ids  = map[string]meshcore.LocalIdentity{}
)

// ident is a node's real Ed25519 identity. Seeds are tried in a fixed order until the public key starts
// with the node's path hash, so packets carry real keys and the hashes still match the made-up mesh.
func ident(hash byte, name string) meshcore.LocalIdentity {
	idMu.Lock()
	defer idMu.Unlock()
	if id, ok := ids[name]; ok {
		return id
	}
	var seed [ed25519.SeedSize]byte
	for n := uint32(0); ; n++ {
		buf := make([]byte, len(name)+4)
		copy(buf, name)
		binary.LittleEndian.PutUint32(buf[len(name):], n)
		seed = sha256.Sum256(buf)
		id := meshcore.NewLocalIdentityFromSeed(seed)
		if id.PublicKey()[0] == hash {
			ids[name] = id
			return id
		}
	}
}

func key(hash byte, name string) [32]byte { return ident(hash, name).PublicKey() }

// channelSecret is the real secret for the simulator's channels: the well-known Public key,
// or SHA-256 of the hashtag for the others, as the firmware and official apps derive them.
func channelSecret(name string) [16]byte {
	var psk [16]byte
	if name == "Public" {
		copy(psk[:], meshcore.PublicChannel().PSK)
		return psk
	}
	return meshcore.DeriveHashtagPSK(name)
}

func simChannels() []radio.Channel {
	out := make([]radio.Channel, len(channelNames))
	for i, n := range channelNames {
		out[i] = radio.Channel{Idx: i, Name: n, Secret: channelSecret(n)}
	}
	return out
}
