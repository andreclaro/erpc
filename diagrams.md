# Admin cache purge — diagrams

Companion visuals for the `erpc_purgeCache` proposal.

## 1. Dispatch flowchart

![Dispatch flowchart](./1-dispatch.png)

## 2. Sequence (happy path)

![Sequence diagram](./2-sequence.png)

## 3. Key anatomy / before → after

Includes `cacheKeySuffix` (e.g. `systx`) and the unsuffixed over-match filter.

![Key anatomy and before/after](./3-keys.png)
