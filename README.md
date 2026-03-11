
<p align="center">
  <img src="./assets/logo-brutal.svg" width="100%">
    A Sudoku-based proxy protocol, ushering in the era of plaintext / low-entropy proxies
</p>

# Sudoku (ASCII)


> Sudoku protocol is now supported by [Mihomo](https://github.com/MetaCubeX/mihomo) kernel!

[![Build Status](https://img.shields.io/github/actions/workflow/status/SUDOKU-ASCII/sudoku/.github/workflows/release.yml?branch=main&style=for-the-badge)](https://github.com/SUDOKU-ASCII/sudoku/actions)
[![Latest Release](https://img.shields.io/github/v/release/SUDOKU-ASCII/sudoku?style=for-the-badge)](https://github.com/SUDOKU-ASCII/sudoku/releases)
[![License](https://img.shields.io/badge/License-GPL%20v3-blue.svg?style=for-the-badge)](./LICENSE)

[中文文档](https://github.com/SUDOKU-ASCII/sudoku/blob/main/README.zh_CN.md)


**SUDOKU** is a traffic obfuscation protocol based on the creation and solving of 4x4 Sudoku puzzles. It maps arbitrary data streams (data bytes have at most 256 possibilities, while non-isomorphic 4x4 Sudokus have 288 variants) into uniquely solvable Sudoku puzzles based on 4 Clues. Since each Puzzle has more than one setting scheme, the random selection process results in multiple combinations for the same encoded data, generating obfuscation.

The core philosophy of this project is to utilize the mathematical properties of Sudoku grids to implement byte stream encoding/decoding, while providing arbitrary padding and resistance to active probing.

## Supported Clients

Version requirements:
- Mihomo-based clients: **Mihomo > v1.19.21** 
- Official Android client Sudodroid: **Sudodroid >= v0.2.4**

### Android
- **CMFA (Clash Meta for Android / Mihomo kernel)**: https://github.com/MetaCubeX/ClashMetaForAndroid
- **Sudodroid (official Sudoku client)**: https://github.com/SUDOKU-ASCII/sudoku-android

### iOS
- **Clash Mi (Mihomo kernel)**: https://github.com/KaringX/clashmi (App Store: https://apps.apple.com/us/app/clash-mi/id6744321968)

### Desktop (Windows/macOS/Linux)
- **Sudoku Desktop (official Sudoku desktop client)**: https://github.com/SUDOKU-ASCII/sudoku-desktop
- **Any Mihomo-kernel GUI wrapper** (e.g. Clash Verge Rev): https://github.com/Clash-Verge-rev/clash-verge-rev

### Routers (OpenWrt)
- **OpenClash**: https://github.com/vernesong/OpenClash

### Install Script (server/tools)
- https://github.com/SUDOKU-ASCII/easy-install

## Core Features

### Sudoku Steganography Algorithm
Unlike traditional random noise obfuscation, this protocol uses various masking schemes to map data streams into complete ASCII printable characters. To packet capture tools, it appears as completely plaintext data. Alternatively, other masking schemes can be used to ensure the data stream has sufficiently low entropy.
*   **Dynamic Padding**: Inserts non-data bytes of arbitrary length at arbitrary positions at any time, hiding protocol characteristics.
*   **Data Hiding**: The distribution characteristics of padding bytes match those of plaintext bytes (65%~100%* ASCII ratio), preventing identification of plaintext through data distribution analysis.
*   **Low Information Entropy**: The overall byte Hamming weight is approximately 3.0* (in low entropy mode), which is lower than the 3.4~4.6 range mentioned in the GFW Report that typically triggers blocking.
*   **User-defined Fingerprints**: You can freely choose your preferred byte style via ASCII/entropy preference and `custom_table`/`custom_tables` layouts. We don’t recommend a single “best” layout — diversity across users helps censorship resistance.

---

> *Note: A 100% ASCII ratio requires the `ASCII-preferred` mode; in `ENTROPY-preferred` mode, it is 65%. A Hamming weight of 3.0 requires `ENTROPY-preferred` mode; in `ASCII-preferred` mode, it is 4.0. Currently, there is no evidence indicating that either preference strategy possesses a distinct fingerprint.

### Downlink Modes
* **Pure Sudoku Downlink**: Default. Uses classic Sudoku puzzles in both directions.
* **Bandwidth-Optimized Downlink**: Set `"enable_pure_downlink": false` to pack AEAD ciphertext into 6-bit groups (01xxxxxx / 0xx0xxxx) with padding reuse. This reduces downlink overhead while keeping uplink untouched. AEAD must be enabled for this mode. Padding pools and ASCII/entropy preferences still influence the emitted byte distribution (downlink is not “random noise”). In practice, downlink efficiency is typically around **80%**.

### Security & Encryption
Beneath the obfuscation layer, the protocol optionally employs AEAD to protect data integrity and confidentiality.
*   **Algorithm Support**: AES-128-GCM or ChaCha20-Poly1305.
*   **PFS + Key Update**: Session keys are derived per connection and rotated during long transfers.
*   **Anti-Replay**: Strict nonce de-duplication within a short TTL (plus timestamp skew checks) blocks window replays.

### Defensive Fallback
When the server detects illegal handshake requests, timed-out connections, or malformed data packets, it does not disconnect immediately. Instead, it seamlessly forwards the connection to a specified decoy address (such as an Nginx or Apache server). Probers will only see a standard web server response.

#### Fallback as a Chained Proxy (Port Sharing)
Fallback is not limited to “decoy web pages”. Since the server forwards the **raw TCP connection** and **replays bytes it already consumed during handshake**, `fallback_address` can also act as a simple **chained-proxy relay**.

A common pattern is “Sudoku-to-Sudoku” chaining:
1. **Inner (real) Sudoku server** listens on a private port (for example `0.0.0.0:8443`) with your real `key` / `aead` / `httpmask` settings.
2. **Outer (entry) Sudoku server** listens on the public port and sets:
   - `"suspicious_action": "fallback"`
   - `"fallback_address": "x.x.x.x:8443"` (points to the inner server `ip:port`)
   - either a **different (fake) `key`** from the inner server, or the **same key but different `ascii` preference**, so normal clients will fail the outer handshake and trigger fallback.
3. **Client** connects to the outer public address, but uses the **inner server’s real key**.

In effect, the client “connects to the outer server”, but the actual Sudoku handshake and tunnel are completed by the inner server; the outer server just replays the already-read prefix and then forwards bytes in both directions.
(~~Yes, the jump box is also a full Sudoku server.~~)

### Drawbacks (TODO)
1.  **Packet Format**: TCP native; UDP is relayed via UoT (UDP-over-TCP) without exposing a raw UDP listener.
2.  **Client Proxy**: Only supports SOCKS5/HTTP. No native TUN.
3.  **Protocol Popularity**: Currently only official and mihomo support, no compatibility with other cores.



## Quick Start

### Build

```bash
go build -o sudoku cmd/sudoku-tunnel/main.go
```

### Configuration

Configuration templates and field explanations:
- [configs/README.md](./configs/README.md)
- [configs/README.zh_CN.md](./configs/README.zh_CN.md)

Templates:
- `configs/server.config.json`
- `configs/client.config.json`

### Docker (Server)
Build locally:
```bash
docker build -t sudoku:local .
```
Run (auto-generate config on first start):
```bash
docker run --rm -p 8080:8080 -p 8081:8081 -v sudoku-data:/etc/sudoku sudoku:local
```

The container writes `/etc/sudoku/server.config.json` and `/etc/sudoku/keys.env` when they don’t exist, and prints the client key in logs.

If you prefer to bring your own config, mount it instead:
```bash
docker run --rm -p 8080:8080 -p 8081:8081 -v "$PWD/server.config.json:/etc/sudoku/server.config.json:ro" sudoku:local
```

**Note**: The Key must be generated specifically by Sudoku.

### Run

> You must generate a KeyPair first
```bash
$ ./sudoku -keygen
Available Private Key: b1ec294d5dba60a800e1ef8c3423d5a176093f0d8c432e01bc24895d6828140aac81776fc0b44c3c08e418eb702b5e0a4c0a2dd458f8284d67f0d8d2d4bfdd0e
Master Private Key: 709aab5f030c9b8c322811d5c6545497c2136ce1e43b574e231562303de8f108
Master Public Key:  6e5c05c3f7f5d45fcd2f6a5a7f4700f94ff51db376c128c581849feb71ccc58b
```
You need to enter the `Master Public Key` into the server configuration's `key` field, then copy the `Available Private Key` into the client configuration's `key` field.

If you want to generate more private keys that fits the public key, you can use the `-more` option and pass the argument with an existing private key("Master Private Key" also works):
```bash
$  ./sudoku -keygen -more 709aab5f030c9b8c322811d5c6545497c2136ce1e43b574e231562303de8f108
Split Private Key: 89acb9663cfd3bd04adf0001cc7000a8eb312903088b33a847d7e5cf102f1d0ad4c1e755e1717114bee50777d9dd3204d7e142dedcb023a6db3d7c602cb9d40e
```

Run the program specifying a config path:
```bash
./sudoku -c server.config.json
./sudoku -c client.config.json
```

## Protocol Flow

1.  **Initialization**: Client and Server generate the same Sudoku mapping table based on the pre-shared Key.
2.  **Handshake**: Client sends encrypted timestamp and nonce.
3.  **Transmission**: Data -> AEAD Encryption -> Slicing -> Mapping to Sudoku Clues -> Adding Padding -> Sending.
4.  **Reception**: Receive Data -> Filter Padding -> Restore Sudoku Clues -> Lookup Table Decoding -> AEAD Decryption.

---


## Disclaimer
> [!NOTE]\
> This software is for educational and research purposes only. Users are responsible for complying with local network regulations.

## Acknowledgements

- [Link 1](https://gfw.report/publications/usenixsecurity23/zh/)
- [Link 2](https://github.com/enfein/mieru/issues/8)
- [Link 3](https://github.com/zhaohuabing/lightsocks)
- [Link 4](https://imciel.com/2020/08/27/create-custom-tunnel/)
- [Link 5](https://oeis.org/A109252)
- [Link 6](https://pi.math.cornell.edu/~mec/Summer2009/Mahmood/Four.html)


## Star History

[![Stargazers over time](https://starchart.cc/SUDOKU-ASCII/sudoku.svg?variant=adaptive)](https://starchart.cc/SUDOKU-ASCII/sudoku)
