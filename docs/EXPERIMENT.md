# A or B? — Experiment Design (v3)

Status: **approved 2026-10-29**.

## 1. What we want to learn

1. **Position:** how often do people pick the first option (left when side by side, top when stacked)? Does that differ by device or layout?
2. **Attributes:** for each color, emphasis (solid, soft, outline), size and label, how often is an option picked when it has that level and its opponent doesn't?
3. **Optimal layout:** which combination of position, color, emphasis and size is most likely to be chosen?
4. **Most one-sided test:** which matchup (for example *red vs. gray*) has the most lopsided split once we have enough data to trust it?
5. **Secondary:** do primes ("Go with your gut" or "Think carefully") change how fast people decide or how much position matters?

We don't show any results to visitors yet. A stats and results module comes later, once we have enough data.

## 2. Visitor flow

```
Start screen                      Trial (×10)                       Block end
┌──────────────────────────┐      ┌──────────────────────────┐      ┌──────────────────────────┐
│  A or B?                 │      │  Pick one.               │      │  That's 10!              │
│  [prime text, if any]    │ ───▶ │  ┌────────┐  ┌────────┐  │ ───▶ │  [ Keep going ]          │
│     [ Are you ready? ]   │      │  │   A    │  │   B    │  │      │  [ I'm done ]            │
│  clicks logged anonymously│     │  └────────┘  └────────┘  │      └──────────────────────────┘
└──────────────────────────┘      └──────────────────────────┘
                                   between trials: 500 ms blank with a centered dot
```

- **"Are you ready?"** is centered horizontally. On desktop, that puts the pointer the same distance from both options when trial 1 appears. The pointer's position at render is logged anyway.
- Ready stays disabled until the brand font has loaded. Otherwise a late font swap would throw off the timing.
- **Timing:** the clock starts at render (after two `requestAnimationFrame` calls) and stops at the click or tap event's `timeStamp`.
- **Keep going** starts another block of 10 trials. There is no limit.
- **I'm done** shows a thank-you screen with a short note on what was varied (no numbers). If they continue after that, their trials are flagged `informed = true`, so analysis can separate them out.
- **Returning visitors** pick up at their next trial number. They keep their visitor-level conditions.

## 3. What gets randomized

### Per option, each trial
Each of the two options gets its own value for every attribute, drawn independently. Sometimes both options get the same value. Those trials act as built-in controls: when color is identical, any preference has to come from something else.

| Attribute | Levels (draft) | Notes |
|---|---|---|
| `color` | red, orange, green, blue, purple, gray | All at the same OKLCH lightness and chroma, so we're measuring **hue**, not brightness. Gray is the neutral control. |
| `emphasis` | solid, soft, outline | Standard CTA treatments, from primary to tertiary. **solid** = full color fill. **soft** = the same color at about 30% opacity. **outline** = a 2px border in that color and no fill. Text always stays at full contrast, so only the fill changes and the label stays readable. |
| `size` | base, +8% | Even the base size gives a tap target of at least 44px. |

Every option uses **one brand font** (self-hosted, so no Google Fonts requests), the way a real product would.

### Per trial (applies to both options)
| Factor | Levels | Notes |
|---|---|---|
| `frame` | tiles, split | **tiles**: two buttons in the middle of the screen. **split**: each option fills half of the play area, left/right or top/bottom. In split, `size` +8% means a 52/48 share of the screen instead of a scale. Added 2026-10-29 to test whether nudges survive when the targets are huge. |
| `labels` | letters (A/B), numbers (1/2), none | With no labels, options differ only in color, emphasis and size. |
| `label_order` | normal (A, then B), reversed (B, then A) | Answers "do people favor A or 1?" separately from position. |
| `default_focus` | none, first, second | The focused option is the one that answers Enter. |

### Per visitor (fixed for the whole visit)
| Factor | Levels |
|---|---|
| `prime` | none, "Go with your gut", "Think carefully before choosing" |

**Dropped for now:** `social_proof`, which moves to the future results module. The old warm/cool `color` factor is also gone, replaced by the hue attribute above.

### Variety
There are 6 × 3 × 2 = 36 possible looks per option, which makes 1,296 ordered pairs. Frame, labels, label order and focus multiply that to about 47,000 different screens, so a visitor doing 50 trials will rarely see a repeat.

## 3a. How much data we need

These are possible combinations, **not trials we need to run**. Each trial counts toward every attribute at the same time, so estimates pool across all combinations.

To measure a level's win rate to within ±5 points (about 385 contested appearances):

| Question | Trials needed | At 10 trials per visitor |
|---|---|---|
| Position bias (every trial counts) | ~400 | ~40 visitors |
| Size: base vs. +8% | ~800 | ~80 visitors |
| Each emphasis level's win rate | ~870 | ~90 visitors |
| Each color's win rate | ~1,400 | ~140 visitors |
| Color head-to-head (e.g. red vs. blue, ±10 points) | ~1,800 | ~180 visitors |
| Optimal layout (model with all attributes at once) | ~2,000 for a first reliable read | ~200 visitors |
| Most one-sided **exact pair** (all attributes fixed) | ~20,000+ | far off; we rank attribute matchups first |

Trimming color from 10 levels to 6 and size from 3 to 2 cut the time to a usable baseline by roughly 40%, compared with v2. To speed it up further, we can drop `default_focus` or a `labels` level.

## 4. How assignment works

Assignment is deterministic and happens on the server:

```
value = SHA-256(version ‖ salt(attribute) ‖ visitor_id ‖ trial_index ‖ slot)  →  first 8 bytes  →  mod n_levels
```

- `slot` (1 or 2) is used only for per-option attributes. It's left out for trial-level factors, and visitor-level factors also leave out `trial_index`.
- `GET /trials?vid=…&start=20&count=10` returns the next block of trials. The same visitor ID and trial number always produce the same trial.
- When an event arrives, the server recomputes the trial and rejects it if it doesn't match.
- Factors, levels and salts all live in `experiments.yaml`. Adding a level or factor is a config change plus a version bump.

## 5. Events

| Event | When | Key fields |
|---|---|---|
| `session_start` | Ready is clicked | prime, start pointer position, whether fonts were ready, session number and days since first visit (for daily returners) |
| `exposure` | a trial renders | trial_index, block_index, both options' attributes, labels, label_order, focus, layout, pointer position at render, informed |
| `choice` | an option is picked | exposure_id, chosen_slot (1 or 2), latency_ms, input type, interaction trace (hover and dwell, or touch, scroll and tap position) |
| `block_end` | after each 10th choice | action: keep_going or done |

Every event also carries: device class, pointer type, viewport size, orientation, layout, light or dark theme, referrer domain, local hour and experiment version.

**Abandonment:** an exposure with no matching choice.

## 6. Data shape

Each attribute gets its own column (`opt1_color`, `opt1_emphasis`, `opt1_size`, `opt2_color`, …, `labels`, `label_order`, `default_focus`, `prime`, `trial_index`, `block_index`). `opt1` is always the option rendered **first**. A `conditions` jsonb column holds the full set for any factors added later.

## 7. Analysis (built as SQL views now; modeling later)

| Question | Method |
|---|---|
| Position bias | Share of choices going to `opt1`, overall and split by layout, device and prime. Trials where the two options are fully identical give the cleanest estimate. |
| Attribute win rates | For each level, among trials where the two options differ on that attribute: the share of times the option with that level was picked. Includes a head-to-head matrix (red vs. blue, and so on). |
| Optimal layout | A conditional logit model: P(pick option 1) = logistic(position + Σ effect × (opt1 level − opt2 level)). Every attribute is estimated at once. The best layout takes the best level of each attribute. |
| Most one-sided test | Rank matchups by the **lower bound** of the 95% confidence interval on the winner's share. A matchup needs at least 30 trials to be ranked. This keeps a 3–0 fluke from beating a solid 70–30. Ranked first by attribute matchup (15 color pairs, 3 emphasis pairs, 1 size pair), then by exact full pair once enough data exists. |
| Within-person effects | Each visitor does many trials, so confidence intervals are clustered by visitor. Trial 1 is also reported on its own, as the cleanest "first impression." Visitors who pick the same side on almost every trial are flagged. |

## 8. Validity tests (Go, run in CI)

- **Determinism:** the same inputs always give the same trial.
- **Balance:** across 100k synthetic trials, each level appears at its expected share (chi-square goodness of fit).
- **Independence:** for every pair of attributes, and for slot 1 vs. slot 2 of the same attribute, the values show no association (chi-square test of independence).
- **Config:** duplicate factors or salts, empty levels or missing salts fail at startup.

## 9. Infrastructure changes from v1

- **Supabase is part of the repo:** `supabase/config.toml` and `supabase/migrations/`. Locally, `supabase start` runs the database (and Studio) in Docker, replacing docker-compose. Deploys use `supabase link --project-ref <ref>` and `supabase db push` against the existing Supabase project. The project ref is a placeholder for now.
- **Secrets:** for local dev, the DB password and connection string go in `.env`, which is gitignored (`.env.example` lists the variables). On Cloud Run, the same values come from Secret Manager.
- **Test, then wipe:** a wipe script clears every event table and keeps the schema. Running it before launch starts real data collection from zero. Locally, `supabase db reset` rebuilds the whole database.
- The per-IP rate limiter lives in memory only and IPs are never stored.
- `/stats` and the results screen are deferred.
