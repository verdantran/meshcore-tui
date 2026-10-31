# Console commands

<!-- Written by go run ./scripts/consoledoc from internal/console and internal/admin/settings.toml; edit those, not this. -->

Two consoles take commands as you'd type them on a radio's serial console:

- **Your own radio:** `c` on the Device screen, or `:radio console`. It needs companion firmware v1.18.0 or newer.
- **A repeater or room server:** the Console pane on the Admin screen, once you're logged in as admin, or a repeater on USB with `--repeater`.

As you type, the rest of a command shows dimmed after the cursor, and `tab` adds it. When several commands match, they're listed under the prompt, and `tab` and `shift+tab` step through them. Once a command is complete, the line underneath says what it does, and what it takes shows after the cursor. Suggestions come only from these lists, never from what you've typed before; `↑` and `↓` go through your history, which is kept for each node separately and forgotten when the app quits.

Commands that take a radio away, could lock you out or change what you can hear ask for confirmation first, and commands carrying a password or PIN are left out of the history.

## Before changing radio settings

Radio rules where you are limit the frequencies you may use, how much power you may transmit and how much of the time you may transmit. **Check your local regulations before changing** any of these, marked *check local regulations* below:

- **Frequency and bandwidth** (`set radio`, `tempradio`, and the presets and radio rows on the Device screen): a signal takes up its frequency plus or minus half the bandwidth, so a wider bandwidth near the edge of a band can spill outside it.
- **Transmit power** (`set tx`, and the Device screen's TX power): limits usually apply to radiated power, which is the setting plus your antenna's gain.
- **The front-end amplifier** (`set radio.fem.txgain`, on boards that have one): it adds power on top of the TX setting.
- **Duty cycle and airtime** (`set dutycycle`, `set af`): some bands limit how much of the time you may transmit.
- **Listening before transmitting** (`set cad`): some rules accept it in place of a duty cycle limit.
- **Repeating** (`set repeat` on a repeater): passing on other nodes' packets adds to your airtime.

The app asks before some of these changes, but it doesn't know your local rules.

## The commands

These are the commands of MeshCore firmware v1.18.0, read from its source. Some depend on the board: WiFi commands only answer on WiFi builds, and the FEM gain only on boards with a front-end module. [MeshCore's CLI reference](https://github.com/meshcore-dev/MeshCore/blob/main/docs/cli_commands.md) has more detail on the shared settings.

### Your own radio

#### Radio

| Command | Takes | What it does |
|---|---|---|
| `get` / `set radio` | `<freq>,<bw>,<sf>,<cr>` | *check local regulations*. All four at once, in MHz and kHz; takes effect after a reboot. You'll only hear nodes using the same. |
| `get freq` |  | The frequency in MHz. |
| `get` / `set tx` | `<dBm>` | *check local regulations*. Transmit power in dBm, up to what the board allows. |
| `get` / `set radio.rxgain` | `on\|off` | The LoRa chip's boosted receive gain; some chips say it's unsupported. |
| `get` / `set radio.fem.rxgain` | `on\|off` | The front-end module's receive amplifier: on hears weaker signals, off can help next to strong transmitters. |
| `get` / `set af` | `<0–9>` | *check local regulations*. The airtime factor. |
| `get` / `set dutycycle` | `<1–100>` | *check local regulations*. The share of time it may transmit, as a percentage; some regions set a legal limit. |
| `get` / `set cad` | `on\|off` | *check local regulations*. Listen before transmitting (channel activity detection). |
| `get` / `set int.thresh` | `<n>` | The interference threshold. |
| `get` / `set agc.reset.interval` | `<n>` | How often the receiver's gain control resets; the radio rounds it. |

#### Timing

| Command | Takes | What it does |
|---|---|---|
| `get` / `set rxdelay` | `<0–20>` | The receive delay base. |
| `get` / `set txdelay` | `<0–2>` | The delay factor before passing on flood packets. |
| `get` / `set direct.txdelay` | `<0–2>` | The delay factor before passing on direct packets. |

#### Messaging

| Command | Takes | What it does |
|---|---|---|
| `get` / `set path.hash.mode` | `0\|1\|2` | The path hash size: 0, 1 or 2 for 1, 2 or 3 bytes. |
| `get` / `set multi.acks` | `<n>` | How many extra acks it sends, so fewer messages look undelivered. |

#### This radio

| Command | Takes | What it does |
|---|---|---|
| `ver` |  | The firmware version and build date. |
| `board` |  | The board's name. |
| `get` / `set name` | `<name>` | The radio's name, as other nodes see it. |
| `set pin` | `<6 digits>` | The Bluetooth pairing PIN; anything paired will need to pair again. |
| `get` / `set tz.offset` | `<hours>` | The time zone offset, for boards with a screen. |
| `reboot` |  | Restarts the radio; the link reconnects by itself. |
| `poweroff` |  | Switches the radio off; someone has to switch it back on. |
| `shutdown` |  | The same as poweroff. |

#### WiFi

| Command | Takes | What it does |
|---|---|---|
| `get` / `set wifi.ssid` | `<name>` | The WiFi network to join; set wifi.pwd too, then reboot. |
| `get` / `set wifi.pwd` | `<password>` | The WiFi password. |
| `set wifi.clear` |  | Forgets the WiFi network. |
| `get` / `set wifi.enabled` | `0\|1` | WiFi off or on; takes effect after a reboot. |
| `get wifi.status` |  | Whether it's connected. |
| `get wifi.ip` |  | Its address on the network. |


### Repeaters and room servers

Marked *repeater* where a room server doesn't have it, and *USB only* where it only answers on the node's own USB console, not over the mesh.

#### The node

| Command | Takes | What it does |
|---|---|---|
| `ver` |  | The firmware version and build date. |
| `board` |  | The board's name. |
| `clock` |  | The node's clock. |
| `clock sync` |  | Sets the node's clock from the time this message was sent. |
| `time` | `<epoch seconds>` | Sets the node's clock; it only moves forwards. |
| `advert` |  | Sends an advert across the mesh now. |
| `advert.zerohop` |  | Sends an advert to direct neighbours only. |
| `reboot` |  | Restarts the node; it's off the mesh until it's back. |
| `clkreboot` |  | Resets the node's clock, then restarts it. |
| `poweroff` |  | Switches the node off; someone has to switch it back on. |
| `shutdown` |  | The same as poweroff. |
| `start ota` |  | Stops routing and waits for a firmware upload. |
| `erase` |  | *USB only*. Wipes the node's storage. It can't be undone. |
| `password` | `<new password>` | The admin password. There's no recovery over the air, so note it down. |
| `tempradio` | `<freq>,<bw>,<sf>,<cr>,<minutes>` | *check local regulations*. Tries radio settings for a while, then goes back by itself: safer than set radio. |
| `get freq` |  | The frequency in MHz. |
| `get bootloader.ver` |  | The bootloader's version, on nRF52 boards. |
| `get` / `set extra.sf` | `<sf>[,<sf>,<sf>]` | Up to three extra spreading factors to listen on, on LR2021 boards. |

#### Power

| Command | Takes | What it does |
|---|---|---|
| `get pwrmgt.support` |  | Whether the board has power management. |
| `get pwrmgt.bootreason` |  | Why the node last reset and last shut down. |
| `get pwrmgt.bootmv` |  | The battery voltage when it last started, on boards with power management. |
| `powersaving` | `[on\|off]` | Shows whether power saving is on, or turns it on or off, on nRF52 boards. |

#### Neighbours

| Command | Takes | What it does |
|---|---|---|
| `neighbors` |  | *repeater*. The nodes it hears directly, with their signal. |
| `discover.neighbors` |  | *repeater*. Asks nearby nodes to answer, to fill the neighbour list. |
| `neighbor.remove` | `<key or prefix>` | *repeater*. Removes every neighbour whose key starts with this. |

#### Access

| Command | Takes | What it does |
|---|---|---|
| `setperm` | `<public key> <0\|1\|2\|3>` | Sets a companion's permission: 0 guest, 1 read-only, 2 read-write, 3 admin. |
| `get acl` |  | *USB only*. The access list. |

#### Regions

| Command | Takes | What it does |
|---|---|---|
| `region` |  | The region tree: ^ marks home, F allows flooding. |
| `region home` | `[name]` | Shows the home region, or sets it. |
| `region put` | `<name> [parent]` | Adds a region, under another if given. |
| `region remove` | `<name>` | Removes a region. |
| `region allowf` | `<name or *>` | Allows flooding in a region. |
| `region denyf` | `<name or *>` | Stops flooding in a region. |
| `region get` | `<name>` | One region, with its parent and whether it allows flooding. |
| `region default` | `[name\|<null>]` | Shows the default flood scope, or sets it; <null> clears it. A name it doesn't have yet is added. |
| `region list` | `allowed\|denied` | The regions that allow flooding, or those that don't. |
| `region save` |  | Saves the region tree, so it survives a restart. |

#### Logging

| Command | Takes | What it does |
|---|---|---|
| `log` |  | *USB only*. Reads the packet log. |
| `log start` |  | Starts logging packets. |
| `log stop` |  | Stops logging packets. |
| `log erase` |  | Erases the packet log. |

#### Statistics

| Command | Takes | What it does |
|---|---|---|
| `clear stats` |  | Resets the counters. |
| `stats-core` |  | *USB only*. Uptime, battery and queue. |
| `stats-radio` |  | *USB only*. Noise floor, signal and airtime. |
| `stats-packets` |  | *USB only*. Packets sent and received. |

#### GPS and sensors

| Command | Takes | What it does |
|---|---|---|
| `gps` |  | GPS status, on boards with GPS. |
| `gps on` |  | Turns the GPS on. |
| `gps off` |  | Turns the GPS off. |
| `gps sync` |  | Sets the clock from the GPS. |
| `gps setloc` |  | Sets the node's location from the GPS. |
| `gps advert` | `[none\|share\|prefs]` | Whether adverts carry the GPS location. |
| `gps diag` |  | GPS diagnostics. |
| `sensor list` |  | The sensors' settings. |
| `sensor get` | `<key>` | One sensor setting. |
| `sensor set` | `<key> <value>` | Changes a sensor setting. |

#### Settings: Identity

| Command | Takes | What it does |
|---|---|---|
| `get` / `set name` | `<text>` | Name: The name other nodes see in adverts. |
| `get` / `set lat` | `<-90–90>` | Latitude: Shared in adverts. Use 0 to keep the location private. |
| `get` / `set lon` | `<-180–180>` | Longitude |
| `get` / `set owner.info` | `<text>` | Owner info: Who runs this node. A \| starts a new line. |
| `get role` |  | Role |
| `get public.key` |  | Public key |

#### Settings: Radio

| Command | Takes | What it does |
|---|---|---|
| `get` / `set radio` | `<freq>,<bw>,<sf>,<cr>` | *check local regulations*. Radio: Frequency MHz, bandwidth kHz, spreading factor, coding rate. A wrong value strands the node off-frequency: try `tempradio` first. |
| `get` / `set tx` | `<1–22 dBm>` | *check local regulations*. Transmit power: Less power shortens its reach: nodes that hear it now, and your way in to it, may lose it. |
| `get` / `set radio.rxgain` | `on\|off` | Boosted receive gain: Off, it can't hear weaker signals, and yours may be one of them. |
| `get` / `set radio.fem.rxgain` | `on\|off` | Front-end receive amplifier: Only on boards with an external front-end module. Off, it can't hear weaker signals, and yours may be one of them. |
| `get` / `set radio.fem.txgain` | `on\|off` | *check local regulations*. Front-end transmit amplifier: Only on boards with an external front-end module. Off, its reach shrinks, and your way in to it may go. |

#### Settings: Routing

| Command | Takes | What it does |
|---|---|---|
| `get` / `set repeat` | `on\|off` | *repeater*, *check local regulations*. Repeat packets: Off, it stops passing on other nodes' packets, so every link through it breaks. |
| `get` / `set path.hash.mode` | `0\|1\|2` | Path hash size: 0, 1 or 2 for 1-, 2- or 3-byte hashes in advert paths. Older firmware only reads 1-byte hashes, so a larger size can cut the node off from nodes running it. |
| `get` / `set loop.detect` | `off\|minimal\|moderate\|strict` | Flood loop detection |
| `get` / `set txdelay` | `<0–2>` | Flood retransmit delay |
| `get` / `set direct.txdelay` | `<0–2>` | Direct retransmit delay |
| `get` / `set rxdelay` | `<0–20>` | Weak-signal receive delay: Experimental. |
| `get` / `set dutycycle` | `<1–100 %>` | *check local regulations*. Duty cycle limit |
| `get` / `set af` | `<0–9>` | *check local regulations*. Airtime factor (deprecated): Replaced by the duty cycle limit. |
| `get` / `set int.thresh` | `<0–255>` | Interference threshold |
| `get` / `set cad` | `on\|off` | *check local regulations*. Listen before transmitting |
| `get` / `set agc.reset.interval` | `<0–1020 s>` | AGC reset interval: Multiples of 4 seconds; 0 turns it off. |
| `get` / `set multi.acks` | `0\|1` | Multiple acks |

#### Settings: Flood

| Command | Takes | What it does |
|---|---|---|
| `get` / `set flood.max` | `<0–64>` | Maximum flood hops: Too low and it stops passing on floods, which is how nodes find a way to each other. |
| `get` / `set flood.max.unscoped` | `<0–64>` | Maximum hops without a region |
| `get` / `set flood.max.advert` | `<0–64>` | Maximum advert hops |
| `get` / `set flood.advert.interval` | `<3–168 h>` | Flood advert interval |
| `get` / `set advert.interval` | `<60–240 min>` | Zero-hop advert interval |

#### Settings: Access

| Command | Takes | What it does |
|---|---|---|
| `get` / `set guest.password` | `<password>` | Guest password |

#### Settings: Power

| Command | Takes | What it does |
|---|---|---|
| `get` / `set adc.multiplier` | `<0–10>` | Battery reading multiplier: 0 uses the board's own calibration. |
| `get pwrmgt.source` |  | Power source |

#### Settings: Bridge

| Command | Takes | What it does |
|---|---|---|
| `get` / `set bridge.enabled` | `on\|off` | Bridge: Only on firmware built with a bridge. |
| `get` / `set bridge.delay` | `<0–10000 ms>` | Bridge delay |
| `get` / `set bridge.source` | `rx\|tx` | Bridge source: Which packets go over the bridge: those received, or those sent. |
| `get` / `set bridge.channel` | `<1–14>` | ESP-NOW channel |
| `get bridge.type` |  | Bridge type |
| `get` / `set bridge.secret` | `<password>` | ESP-NOW secret: Shared by every node on the bridge. |
| `get` / `set bridge.baud` | `<9600–115200 baud>` | Serial bridge speed: Only on firmware built with a serial (RS232) bridge. |

#### Room servers only

| Command | Takes | What it does |
|---|---|---|
| `get` / `set allow.read.only` | `on\|off` | Allow read-only guests |
