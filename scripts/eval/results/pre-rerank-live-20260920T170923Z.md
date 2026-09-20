# Eval run: pre-rerank-live

- Queries: 15 (evaluated 15, errors 0, empty 0)
- top_k: 8, junk-filter: on
- hit@1 53%, hit@3 67%, hit@8 87%, MRR 0.721
- heading-path hits among title-hits: 0/13

## Per category

| category | n | hit@1 | hit@3 | hit@8 | MRR |
|---|---|---|---|---|---|
| conceptual | 5 | 20% | 60% | 80% | 0.521 |
| drift | 5 | 40% | 40% | 80% | 0.573 |
| exact | 5 | 100% | 100% | 100% | 1.000 |

## Junk-filter impact (raw top-8 slots)

- junk-flagged results in raw top-8: 8/120 slots (7% of top-8 noise)

## Per query

- exact-01 [exact]: rank 1 -> Developing Apps with GPT-4 and ChatGPT
- exact-02 [exact]: rank 1 -> Get Programming with Go
- exact-03 [exact]: rank 1 -> Pandas Brain Teasers
- exact-04 [exact]: rank 1 -> Code Like a Pro in C#
- exact-05 [exact]: rank 1 -> cpluspluscrashcourse
- concept-01 [conceptual]: rank 2 -> Kanban in Action
- concept-02 [conceptual]: rank 3 -> The Art of Unit Testing, Second Edition
- concept-03 [conceptual]: rank 4 -> Microservices in Action
- concept-04 [conceptual]: rank miss -> -
- concept-05 [conceptual]: rank 1 -> Programming with Types
- drift-01 [drift]: rank 1 -> Learning SQL
- drift-02 [drift]: rank 6 -> Becoming a Better Programmer
- drift-03 [drift]: rank 8 -> Analysis Patterns
- drift-04 [drift]: rank 1 -> Java Pocket Guide
- drift-05 [drift]: rank miss -> -
