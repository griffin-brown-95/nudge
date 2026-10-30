# Designing experiments: a field guide using this repo

This guide walks through how **A or B?** was designed and built, using the
`frame` factor (tiles vs. split screen) as a worked example. Every concept points
to the file that implements it, so you can read the explanation and then the code.

It's written with growth and experimentation **infrastructure** work in mind:
backend services that assign users to variants, log exposures and events, and
make the results trustworthy. That's a different job from a data scientist's,
who mostly analyzes results. The platform engineer's job is making sure the
data is *correct* before anyone analyzes it.

---

## 1. Every experiment has two halves

| Half | Question it answers | In this repo |
|---|---|---|
| **Design** | What are we testing, on whom, and what counts as a result? | [EXPERIMENT.md](EXPERIMENT.md), [experiments.yaml](../nudge/experiments.yaml) |
| **Platform** | How do we assign people, record what they saw and did, and make sure nothing is corrupted? | [assign](../nudge/internal/assign/assign.go), [events](../nudge/internal/events/events.go), [store](../nudge/internal/store/postgres.go), [web](../web/app.js) |

Most experiments that go wrong fail on the **platform** side, not the design
side. The data looked fine but was quietly biased: a variant that crashed
before logging, a cache that served the wrong variant, events counted twice.
Most of this guide is about preventing that.

---

## 2. Worked example: adding `frame`

### Step 1: Write the question before the code

> *Do the nudges (position, color, emphasis, size) still work when the targets
> are huge, or do they mostly come from how easy a button is to hit?*

A good experiment question names:
- **What varies** (the *treatment*): tiles vs. split screen.
- **What you measure** (the *outcome*): which option is picked, and how fast.
- **What would change your mind:** if position bias is the same in both frames,
  it's a decision bias. If it shrinks in split, part of it came from how far
  the pointer had to travel.

Writing this first stops you from adding a factor "because it's cool" and then
not knowing how to read the result.

### Step 2: Pick the unit of randomization

The **unit** is the thing that gets independently assigned. Here there are
three ([config.go](../nudge/internal/config/config.go), `Scope`):

| Scope | Example | Why at this level |
|---|---|---|
| `visitor` | `prime` | A prime ("Go with your gut") is a mindset; switching it every trial would muddle it |
| `trial` | `frame`, `labels` | Changing per trial gives many comparisons per person, so you need far fewer visitors |
| `option` | `color`, `size` | Each button is styled on its own |

`frame` is per trial because it's cheap (every trial counts), and switching
between frames doesn't spoil the next trial the way switching primes would.

**In product A/B testing the unit is almost always the user.** A user who
sees the new checkout on Monday and the old one on Tuesday has a broken
experience, and their behavior on Tuesday is contaminated by Monday. That
contamination is called *carryover*. When one user's treatment affects
another user's outcome, that's *interference* (formally, a violation of
SUTVA, the "stable unit treatment value assumption"). Both are reasons to
randomize at a coarser level than you'd like.

### Step 3: Make each level mean the same thing everywhere

A subtle design decision: in the tiles frame, `size: plus8` scales the button
by 8%. In split, a half-screen button can't grow, so `plus8` instead becomes
a **52/48 share of the screen** ([styles.css](../web/styles.css), `--grow`).

Without that, `size` would mean one thing in tiles and nothing at all in
split, and you couldn't compare it across frames. This is called **construct
validity**: does the thing you manipulated actually match the concept you
claim to be testing?

There's a trade-off, and it should be written down rather than hidden. In split
mode, the bigger option pushes the dividing line about 11px toward the smaller
one. So the start position (the Ready button) is no longer exactly on the line.
Moving the line is what "bigger" means here, so it's acceptable, but the
analysis should know about it.

### Step 4: Assignment is a pure function

```
level = SHA-256(version, salt, visitor_id, trial_index, slot)[:8]  mod  n_levels
```

([assign.go](../nudge/internal/assign/assign.go))

This one line carries most of the platform's correctness guarantees:

- **Deterministic:** the same inputs always give the same level. No
  database lookup, no stored assignment table, no race conditions. Any server
  can compute it, and so can the analysis later.
- **Independent:** each factor has its own **salt**, so knowing a visitor's
  color tells you nothing about their frame. Adding `frame` with a new salt
  didn't change anyone's existing assignments, because every other factor's
  hash is untouched.
- **Versioned:** `version` is part of the hash. Bumping it reshuffles
  everyone. You do that when you change levels, so that old and new data
  don't mix.

**This is how commercial platforms work too.** Eppo, Statsig, GrowthBook and
LaunchDarkly all hash a *subject key* (user ID) together with a *flag or
experiment key* into a fixed number of buckets (often 10,000), then map
bucket ranges to variants. Using buckets instead of `mod n` lets you do
**traffic allocation**, such as "10% of users in the experiment, split 50/50",
and ramp up gradually without reshuffling people. That's exercise 2 below.

### Step 5: Log exposure when it actually happened

An **exposure** means "this person actually saw variant X." It is *not* the
same as "this person was assigned variant X."

In [app.js](../web/app.js), the exposure is sent only after two
`requestAnimationFrame` callbacks, which is when the options have painted on
screen:

```js
requestAnimationFrame(() => requestAnimationFrame(() => {
  t0 = performance.now();      // the clock starts when they can see it
  send({ type: "exposure", ... });
}));
```

Why it matters:
- **Dilution:** if you log at assignment time, people who left before the page
  rendered count as "exposed but did nothing." That drags every variant's
  numbers toward zero and hides real effects.
- **Bias:** if one variant is slower to render (a bigger image, say), more of
  its users leave before seeing it. Logging at assignment then makes that
  variant look worse for a reason that has nothing to do with the design.

Exposure logging is a core concept in platforms like Eppo, where you
implement an *assignment logger* that the SDK calls. Where and when that
logger fires is one of the most important decisions in the whole system.

### Step 6: The server doesn't trust the client

When an exposure arrives, the server **recomputes** the trial from the visitor
ID and trial index. It rejects the event if what the client says it rendered
doesn't match the assignment ([events.go](../nudge/internal/events/events.go),
`checkExposure`).

That catches real bugs: a cached old page, a frontend that misreads a level, a
config deployed to the server before the client updates. In a large platform,
the equivalent is checking that SDK-reported assignments match the
server-side assignment, and alerting when they diverge.

### Step 7: Events are delivered at least once, so make inserts idempotent

Networks fail. The browser queue ([queue.js](../web/queue.js)) **retries until
it gets a definite answer**, which means the server will sometimes receive
the same event twice. So:

- Every event carries an ID the client generates (`exposure_id`, `session_id`).
- Every insert is `ON CONFLICT DO NOTHING`
  ([postgres.go](../nudge/internal/store/postgres.go)), so a retry stores nothing new.
- Events must arrive **in order**. A choice that shows up before its exposure
  gets `409`, meaning "parent not here yet, try later."

At-least-once delivery plus idempotent writes adds up to *effectively once*.
This pattern shows up everywhere in event tracking: Kafka consumers, webhook
receivers, analytics SDKs.

### Step 8: Store it so it's easy to analyze and easy to extend

`frame` got its own column in `exposures`
([migration](../supabase/migrations/20261029000001_init.sql)). The full set of
conditions is *also* stored as jsonb in `conditions`. Explicit columns make
analysis queries simple. The jsonb copy means a new factor is captured from
day one, before anyone has written a migration for it.

**About migrations:** I added `frame` by editing the existing migration files.
That's only acceptable because they have never been applied to a shared
database. **Once a migration has run in production, never edit it.** Write a
new one (for example `alter table exposures add column frame text`), or the
production schema and the files will drift apart.

### Step 9: Test the randomization before collecting data

These are the most important tests in the repo
([assign_test.go](../nudge/internal/assign/assign_test.go)). `frame` was covered
automatically the moment it was added to the config:

| Test | What it proves |
|---|---|
| Determinism | The same visitor and trial always get the same levels |
| Balance | Across 100k trials, `tiles` and `split` each get about 50% (chi-square goodness of fit) |
| Independence | `frame` isn't associated with any other factor (chi-square on every pair; there are now 91) |
| Negative controls | The tests *do* fail on a 51/49 skew, a 5% correlation, or two factors sharing a salt |

The negative controls matter. A test that can't fail proves nothing.

**In production this becomes the SRM check** (sample ratio mismatch). If you
designed a 50/50 split and the live exposure counts come out 50.6/49.4 over a
million users, the chi-square p-value is tiny and **the experiment is
broken**. Usually one variant is crashing, redirecting or failing to log.
Platforms run SRM checks automatically and refuse to show results when they
fail. It's the same chi-square test as `TestBalance`, run on live data.

### Step 10: Analyze honestly

The views ([analysis migration](../supabase/migrations/20261029000002_analysis_views.sql))
now split results by frame:

```sql
select * from v_position_bias where frame is not null and layout is null;
select * from v_attribute_win_rates where attribute = 'color' and frame = 'split';
```

The statistical habits built into them:

- **Confidence intervals, not just percentages.** "62% picked red" from 8
  trials means almost nothing. The **Wilson interval** gives a range the true
  rate is probably in (for example 31%–86%), and it behaves well at small
  sample sizes and near 0% or 100%.
- **Rank by the pessimistic bound.** `v_matchups` ranks how one-sided a
  matchup is by the *lower* end of its interval, so a lucky 3–0 can't beat a
  solid 70–30.
- **Multiple comparisons.** Test 91 factor pairs at the usual 5% level and
  you'd expect about 4–5 false alarms even when nothing is wrong. The tests
  divide the threshold by the number of comparisons (the Bonferroni
  correction).
- **Sample size first.** [EXPERIMENT.md §3a](EXPERIMENT.md) estimates how many
  trials each question needs *before* collecting data. Adding `frame` didn't
  change the overall answers, but anything asked "within split" gets half
  the data.
- **Don't peek and stop.** If you check results every day and stop as soon as
  p < 0.05, your false-positive rate climbs well above 5%. Either fix the
  sample size in advance, or use sequential methods built for continuous
  monitoring. Platforms like Eppo and Statsig offer both.
- **Repeated measures.** One visitor does many trials, so their trials aren't
  independent. Proper intervals *cluster by visitor*. That's a follow-up for
  the analysis module.

---

## 3. Concepts you'll hear in an experimentation platform team

| Term | Meaning | Where it shows up here |
|---|---|---|
| **Feature flag** | A runtime switch that controls what a user sees. An experiment is a flag whose assignment is randomized and logged | `experiments.yaml` is a tiny flag config |
| **Assignment / bucketing** | Mapping a user to a variant, deterministically | `assign.go` |
| **Exposure** | The user actually encountered the variant | `exposure` event, sent after paint |
| **Traffic allocation** | Only X% of users enter the experiment | Not built yet (exercise 2) |
| **Holdout** | A group kept out of *all* changes, to measure their cumulative effect | Not built yet (exercise 3) |
| **Mutual exclusion / layers** | Keeping users out of two experiments that would interfere | This repo does the opposite on purpose: **factorial**, where every factor is crossed with every other |
| **Factorial design** | Many factors randomized independently at once, so every visit informs every factor | The whole design |
| **SRM** | Sample ratio mismatch: the split isn't what you designed, so the experiment is broken | `TestBalance` offline; exercise 1 online |
| **Guardrail metric** | A metric that must not get worse (crash rate, latency) even if the main metric improves | `render_delay_ms`, abandonment (exposure with no choice) |
| **Novelty effect** | Users react to something because it's *new*, and the effect fades | Very relevant for daily players: compare `session_number` 1 vs. 10 |
| **CUPED** | Variance reduction using each user's pre-experiment behavior, so you need less data | Possible later, using a visitor's earlier blocks |
| **Warehouse-native** | Analysis runs as SQL in your own data warehouse instead of a vendor's copy | Exactly what the `v_*` views are: a miniature version |

---

## 4. Exercises: extend this repo

Each of these is a realistic platform task and a good thing to talk through
in an interview.

1. **SRM monitor.** Add a `v_srm` view that compares observed exposure counts
   per level with the expected equal split, and computes a chi-square
   statistic per factor. Add a `GET /health/srm` endpoint that returns 503 if
   any factor fails. *Teaches: online validity checks and alerting.*
2. **Traffic allocation and weights.** Change assignment to hash into 10,000
   buckets and let `experiments.yaml` specify weights (`split: 0.2`).
   Make the balance test check the *weighted* expected shares.
   *Teaches: how real bucketing works, and ramping up safely.*
3. **Holdout.** Reserve 5% of visitors (by a separate salt) who always get a
   fixed "plain" layout. *Teaches: holdouts and measuring cumulative effects.*
4. **Flag endpoint.** Add `GET /flags?vid=` that returns non-experiment config
   (for example, whether the results page is enabled) with the same
   deterministic logic. *Teaches: flags and experiments as one system.*
5. **Infrastructure as code.** Write Terraform for the Cloud Run service,
   Secret Manager secret and log exclusion. *Teaches: GCP, Terraform.*
6. **Load test.** Use `vegeta` or `k6` against `/event` with retries and
   duplicates mixed in, and confirm row counts stay exact. *Teaches: proving
   idempotency under load.*
7. **Clustered intervals.** Compute position-bias intervals that account for
   repeated trials per visitor (a cluster bootstrap in SQL or Python).
   *Teaches: correct inference with repeated measures.*

---

## 5. Explaining this project in two minutes

> I built a factorial experiment platform for a "pick A or B" game. A Go
> service on Cloud Run assigns each visitor's trials with a salted hash, so
> it's deterministic, independent per factor, and needs no database lookups.
> The frontend logs an exposure only after the options have painted, and
> sends events through a durable queue. The server recomputes each trial and
> rejects any client that rendered something it wasn't assigned. Inserts are
> idempotent, so retries are safe. Before collecting any data, I tested the
> randomizer: balance and pairwise independence across 100k synthetic
> trials, with negative controls proving those tests catch real bugs. In
> production, the same check is an SRM monitor. Analysis is warehouse-native
> SQL with Wilson intervals, and matchups are ranked by their pessimistic
> bound so small samples can't win.

Then be ready to go deeper on whichever part they ask about. Steps 4–7 and 9
are the core of what an experimentation infrastructure engineer owns.
