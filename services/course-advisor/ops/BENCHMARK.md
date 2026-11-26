# Synthetic lab performance notes

Windows host, Docker MySQL 8.4.13, Redis 7.4.12; all requests used the synthetic schedule and deterministic rules. A 10,000-row history fixture measured the same 20 items at offset 9,000. Three warm requests and 30 measurements per query gave:

| Query | P50 ms | P95 ms |
|---|---:|---:|
| Original join and sort | 43.54 | 46.01 |
| Original query ignoring the history index | 40.14 | 43.73 |
| Covering index, then fetch 20 full rows | 4.57 | 5.18 |

The first plan used a different index and joined the 10,000 candidates before sorting. The final one still read 9,020 ordered index entries for the offset, but used primary-key lookup on 20 final rows. Run `TestMySQLHistoryExplain` with `ADVISOR_INDEX_REPORT` set to reproduce. Timing is short local evidence, not a production SLO.

`tests/load.cjs` uses official synthetic snapshot only, records read, enqueue and completion separately, and keeps per-round status counts. It is safe to compare only cases with the same payload, operation and admission semantics. Run against fresh dedicated test instances with a JSON target map in `ADVISOR_LOAD_TARGETS`; do not aim it at shared infrastructure.
