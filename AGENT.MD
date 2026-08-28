# Agent Guide

Build with Go 1.23+. Keep PS2, PS3, and PS5 isolated with independent queues. PS5 uses etaHEN FTP :2121 and `/data/etaHEN/games`; support ShadowMountPlus folder, ffpkg, exfat, ffpfs, and ffpfsc games. Stay Docker-safe: never access raw USB devices. Browser assets must be same-origin. Cache PS2 covers under `<PS2_GAME_DIR>/covers`. Run gofmt, vet, test, and build.
