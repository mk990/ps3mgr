# Agent Guide

Build with Go 1.23+. Keep PS2–PS5 isolated with independent queues. PS4 uses flatZ RPI :12800 and a local, tokenized PKG server; never expose arbitrary paths. PS5 uses etaHEN FTP :2121 and ShadowMountPlus formats. Stay Docker-safe: never access raw USB devices. Browser assets must be same-origin. Cache PS2 covers under `<PS2_GAME_DIR>/covers`. Run gofmt, vet, test, and build.
