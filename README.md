
<p align="center">
  <img src="./assets/logo-brutal.svg" width="100%">
    A Sudoku-based proxy protocol, ushering in the era of plaintext / low-entropy proxies
</p>

# Sudoku (ASCII)


> Sudoku protocol is now supported by [Mihomo](https://github.com/MetaCubeX/mihomo) kernel!

[![Build Status](https://img.shields.io/github/actions/workflow/status/saba-futai/sudoku/.github/workflows/release.yml?branch=main&style=for-the-badge)](https://github.com/saba-futai/sudoku/actions)
[![Latest Release](https://img.shields.io/github/v/release/saba-futai/sudoku?style=for-the-badge)](https://github.com/saba-futai/sudoku/releases)
[![License](https://img.shields.io/badge/License-GPL%20v3-blue.svg?style=for-the-badge)](./LICENSE)

[中文文档](https://github.com/saba-futai/sudoku/blob/main/README.zh_CN.md)


**SUDOKU** is a traffic obfuscation protocol based on the creation and solving of 4x4 Sudoku puzzles. It maps arbitrary data streams (data bytes have at most 256 possibilities, while non-isomorphic 4x4 Sudokus have 288 variants) into uniquely solvable Sudoku puzzles based on 4 Clues. Since each Puzzle has more than one setting scheme, the random selection process results in multiple combinations for the same encoded data, generating obfuscation.

The core philosophy of this project is to utilize the mathematical properties of Sudoku grids to implement byte stream encoding/decoding, while providing arbitrary padding and resistance to active probing.

## Android Client：

**[Sudodroid](https://github.com/saba-futai/sudoku-android)**

## Core Features

### Sudoku Steganography Algorithm
Unlike traditional random noise obfuscation, this protocol uses various masking schemes to map data streams into complete ASCII printable characters. To packet capture tools, it appears as completely plaintext data. Alternatively, other masking schemes can be used to ensure the data stream has sufficiently low entropy.
*   **Dynamic Padding**: Inserts non-data bytes of arbitrary length at arbitrary positions at any time, hiding protocol characteristics.
*   **Data Hiding**: The distribution characteristics of padding bytes match those of plaintext bytes (65%~100%* ASCII ratio), preventing identification of plaintext through data distribution analysis.
*   **Low Information Entropy**: The overall byte Hamming weight is approximately 3.0* (in low entropy mode), which is lower than the 3.4~4.6 range mentioned in the GFW Report that typically triggers blocking.

---

> *Note: A 100% ASCII ratio requires the `ASCII-preferred` mode; in `ENTROPY-preferred` mode, it is 65%. A Hamming weight of 3.0 requires `ENTROPY-preferred` mode; in `ASCII-preferred` mode, it is 4.0.

> Currently, there is no evidence indicating that either preference strategy possesses a distinct fingerprint.

---

### Uplink/Downlink Separation
#### —— Attempting to solve downlink bandwidth issues using the API provided by [mieru](https://github.com/enfein/mieru/tree/main)
> Special thanks to the developers of [mieru](https://github.com/enfein/mieru/tree/main)

Since the Sudoku protocol's encapsulation of streams leads to increased packet size, bandwidth limitations may occur in streaming and download scenarios (theoretically, 200mbps symmetric uplink/downlink on local and VPS ends should not cause bottlenecks). Therefore, the `mieru` protocol, which is also a non-TLS solution, is adopted as an (optional) downlink protocol.

#### mieru Configuration
```json
"enable_mieru": true,
"mieru_config": {
    "port": 20123,
    "transport": "TCP",
    "mtu": 1400,
    "multiplexing": "HIGH"
}
```
**Explanation**: When `enable_mieru` is enabled but the `"mieru_config"` field is not configured, it defaults to using the UDP protocol on the same port as Sudoku. When `"enable_mieru"` is `true`, uplink/downlink separation is enabled; when `false`, the `"mieru_config"` field can be ignored. The mandatory field in `"mieru_config"` is `port`, specifying the downlink port; other configurations can be deleted directly.


**Note**: It is currently uncertain whether the traffic characteristics resulting from this configuration will be censored, so it is tentatively listed as an `Experimental Feature`.

### Security & Encryption
Beneath the obfuscation layer, the protocol optionally employs AEAD to protect data integrity and confidentiality.
*   **Algorithm Support**: AES-128-GCM or ChaCha20-Poly1305.
*   **Anti-Replay**: The handshake phase includes timestamp validation, effectively preventing replay attacks.

### Defensive Fallback
When the server detects illegal handshake requests, timed-out connections, or malformed data packets, it does not disconnect immediately. Instead, it seamlessly forwards the connection to a specified decoy address (such as an Nginx or Apache server). Probers will only see a standard web server response.

### Drawbacks (TODO)
1.  **Packet Format**: Only supports TCP data packets.
2.  **Bandwidth Utilization**: Less than 30%. It is recommended for users with high-quality lines or high bandwidth. Additionally, it is recommended for VPN service providers ("Airport owners"), as it can effectively increase user traffic usage stats.
3.  **Client Proxy**: Only supports SOCKS5/HTTP.
4.  **Protocol Popularity**: Currently only official and mihomo support, no compatibility with other cores.



## Quick Start

### Build

```bash
go build -o sudoku cmd/sudoku-tunnel/main.go
```

### Server Configuration (config.json)

```json
{
  "mode": "server",
  "local_port": 1080,
  "server_address": "",
  "fallback_address": "127.0.0.1:80",
  "key": "See the running steps below",
  "aead": "chacha20-poly1305",
  "suspicious_action": "fallback",
  "ascii": "prefer_entropy",
  "padding_min": 2,
  "padding_max": 7,
  "disable_http_mask": false,
  "enable_mieru": false,
  "mieru_config": {}
}
```

### Client Configuration

Change `mode` to `client`, set `server_address` to the Server IP, set `local_port` to the proxy listening port, add `rule_urls` using the template in `configs/config.json`, and configure the uplink/downlink separation according to the template.

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

Run the program specifying the `config.json` path as an argument:
```bash
./sudoku -c config.json
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

[![Star History Chart](https://api.star-history.com/svg?repos=saba-futai/sudoku&type=Date)](https://star-history.com/#saba-futai/sudoku)
