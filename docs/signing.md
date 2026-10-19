# Signed channel messages

> **Experimental.** Signing follows [meshcore-companion's SIGNING.md](https://github.com/edleeman17/meshcore-companion/blob/main/SIGNING.md), sent as a datagram of MeshCore's experimental data type `0xFFFF`. That will change once MeshCore allocates a type for it, and the format may change with it. Other apps ignore the datagram and show the message as usual.

On a channel, anyone with the channel's secret can post under any name. Signing lets a message carry proof of which radio sent it, so someone posting as "Bob" with a different radio stands out.

## What it proves

A signature proves which **key** sent a message, not who owns that key. So the app ties names to keys: once you've pinned Bob's name to Bob's key, a message signed with any other key and sent as Bob is flagged. The key is your radio's own, and the radio does the signing, so the private key never leaves it.

## Sending signed messages

`S` on an open channel signs your messages there, and the header says `✍ signing your messages`. While writing, `ctrl+s` signs or unsigns just that message. Signing is off by default, because each signed message is two packets: the message itself, then a small datagram with the signature.

It needs companion firmware v1.15 or later. If the radio can't sign, the message goes unsigned with a warning, and the Device screen's Security group says why.

## Checking them

Checking is always on, since it costs nothing. A signed message shows one of these:

| Mark | Means |
|---|---|
| `✓ signed` | Signed with the key pinned to the sender's name |
| `signed · not pinned` | A valid signature, from a name with no pin yet; `K` on the picked message pins it |
| `⚠ not Bob's key` | Valid, but not the key Bob is pinned to |
| `⚠ signed with Mallory's key` | Valid, by a key you know under another name |
| `⚠ bad signature` | The signature doesn't match the message |
| `signed by an unknown key` | From a key you've never heard advertise |
| `⚠ unsigned · Bob usually signs` | No signature 30 seconds after the message, from a name that's pinned; without this, an impersonator would simply not sign |

## Pinning names to keys

Pins are kept in the message database by name, shared by every radio you use. `K` on Contacts pins a contact's own key to their name, which is the safest way, since it doesn't trust whichever signed message arrived first. `K` on a picked signed message pins its key; replacing an existing pin asks first.

## On the air

After the message, a 79-byte datagram goes to the same channel:

| Bytes | Field |
|---|---|
| 3 | `MC1` |
| 8 | the first 8 bytes of the signer's public key |
| 4 | the message's timestamp, little-endian, which pairs the datagram with its message |
| 64 | an Ed25519 signature |

The signature covers the tag `MeshCore-Companion signed channel v1`, the channel's hash (the first byte of SHA-256 of its name), the timestamp, and the message as typed, without the `Name: ` the radio puts in front of it. A datagram is checked against every full key the app knows that starts with its 8 bytes.

Which channels you sign on is saved in the config as `sign_channel_ids`; see [Configuration](configuration.md).
