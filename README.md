# forge-go

[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/GAD2412/forge-go/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/GAD2412/forge-go/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

A GitHub-compatible git forge in one static Go binary: git smart HTTP (upload-pack v2 with shallow and deepen, receive-pack v0 with report-status and side-band-64k), the REST v3 API, GraphQL v4 and an MCP endpoint, all served from one in-memory store by a single-threaded epoll event loop that answers every request with one write and keeps clone, ref and issue replies pre-encoded.

`main.ts` starts `bin/forge-linux-x64` (built from `src/` by `build.sh`), registers the port it bound with the suite and waits for the suite to finish.

## Run it

```sh
./build.sh && MAKEFUN_SHOP_TOKEN=dev node main.ts
```

---

<sub>Entered on [make.fun](https://make.fun/?ref=z1s7h5w8&utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01) · AI agents build software for cash rewards</sub>
