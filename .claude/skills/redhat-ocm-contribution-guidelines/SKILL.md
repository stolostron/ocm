---
name: redhat-ocm-contribution-guidelines
description: >
  Red Hat contribution guidelines for the Open Cluster Management (OCM) community repos.
  TRIGGER — use before finalizing a commit message, PR title, PR description, or GitHub
  issue for any of these repos: open-cluster-management-io/ocm, stolostron/ocm (this repo),
  open-cluster-management-io/cluster-proxy, stolostron/cluster-proxy,
  open-cluster-management-io/managed-serviceaccount, stolostron/managed-serviceaccount,
  open-cluster-management-io/cluster-permission, stolostron/cluster-permission,
  open-cluster-management-io/dynamic-scoring-framework, stolostron/dynamic-scoring-framework,
  open-cluster-management-io/enhancements, or any other stolostron/* repo that is a fork of
  an open-cluster-management-io addon/component. Also trigger when a Red Hatter asks
  something like "how do I contribute this ACM/OCP/HCM Jira fix upstream", "can I reference
  this Jira ticket in the PR", "should this be a GitHub issue first", "should I post this in
  the OCM Slack", "should we create a new repo for X", or "should this be ACM-specific or work
  for any OCM adopter". Use it to sanitize internal references (Jira IDs, customer/case names,
  internal links) out of anything public, to decide whether the change needs an issue, a
  design review, an enhancement proposal, and/or a community Slack post before/alongside the
  PR, and to nudge (not gate) the solution design toward vendor/platform-agnostic where that's
  low-cost.
  SKIP when the change is not destined for one of the repos listed above (purely
  ACM-internal code, internal tooling, internal docs) — this guidance does not apply there.
---

# Red Hat → OCM Community Contribution Guidelines

## North star: this speeds the community up, it does not slow you down

The only purpose of this guide is to make sure Red Hat's OCM contributions also grow the
community — more reviewers, more external contributors, more trust that OCM is a real
multi-vendor project and not "just Red Hat's repo with extra steps." It is a light checklist,
not a gate.

For the vast majority of changes (bug fixes, docs, small features, dependency bumps) this
guide adds almost no overhead: sanitize the text, open the PR, done. The heavier steps
(issue-first, design review, enhancement proposal, Slack post) are reserved for genuinely
significant changes — see the trivial/non-trivial table below. When in doubt, do the smaller
amount of process, not the larger amount.

## Scope: which repos this applies to

This applies to changes headed for a community-owned OCM repo, whether opened directly
against upstream or staged through a downstream/stolostron fork:

| Community (upstream)                                  | Downstream fork                     |
|---------------------------------------------------------|----------------------------------------|
| `open-cluster-management-io/ocm`                         | `stolostron/ocm` (this repo)          |
| `open-cluster-management-io/cluster-proxy`               | `stolostron/cluster-proxy`            |
| `open-cluster-management-io/managed-serviceaccount`      | `stolostron/managed-serviceaccount`   |
| `open-cluster-management-io/cluster-permission`          | `stolostron/cluster-permission`       |
| `open-cluster-management-io/dynamic-scoring-framework`   | `stolostron/dynamic-scoring-framework` |
| `open-cluster-management-io/enhancements` (proposal repo) | n/a — proposals are filed directly upstream |

This list isn't exhaustive — new OCM addons show up over time. The same pattern applies to
any repo that is a genuine `open-cluster-management-io` community project with a `stolostron`
fork: contribute upstream first, and treat the downstream fork as a staging point, not the
destination.

**Rule of thumb:** if the code you're touching isn't going into one of the repos above (or a
repo following the same upstream/downstream pattern), this guide almost certainly doesn't
apply — that's ACM-internal work, skip everything below. The one exception is if the work is
large enough that you're weighing standing up a **brand new repo** — see
[Proposing a new repo](#proposing-a-new-repo) before doing that.

## Rule #1: sanitize before it's public

Never put these in a commit message, PR title, PR description, code comment, or GitHub issue
in one of the repos above:

- Internal Jira IDs (`ACM-1234`, `OCPBUGS-5678`, `RFE-...`, `HCM-...`, or any other internal
  project key you use)
- Links to `issues.redhat.com` or any other internal tracker
- Customer names, account names, or case/support-ticket numbers
- Internal Slack channel names/links, or internal-only docs (Google Docs / Confluence /
  Drive links restricted to `@redhat.com`)

Describe the underlying, user-facing problem or change in plain terms instead. If you want
traceability between the internal ticket and the public change, do it in one direction only:
paste the GitHub issue/PR URL **into** the internal Jira ticket. Never paste the Jira ID into
the public PR/issue/commit.

**Before → After**

- ❌ `Fix ACM-48213: registration agent panics on nil clientCert`
  ✅ `Fix panic in registration agent when client certificate is nil`
- ❌ `Customer X hit this in case 03921831 when scaling to 500 clusters`
  ✅ `Placement controller becomes slow with 500+ managed clusters`
- ❌ PR description: "See internal doc: https://docs.google.com/... (redhat.com only)"
  ✅ PR description: inline the relevant design points, or link a public GitHub
    issue/enhancement instead

## Quick sanitize checklist (run this before you hit "Create")

1. Scan the draft commit message / PR title / PR description / issue text for a pattern like
   `[A-Z]{2,}-[0-9]+`. If it matches an internal project prefix (`ACM`, `OCPBUGS`, `RFE`,
   `HCM`, or any other internal project prefix your team uses), remove or rephrase it.
2. Search for `redhat.com`, `issues.redhat.com`, or any customer/account identifier — remove.
3. Re-read the text as if a stranger with no Red Hat context is reading it. Does it stand on
   its own?
4. Confirm the commit is DCO-signed off (`git commit -s`) per this repo's
   [CONTRIBUTING.md](../../../CONTRIBUTING.md).
5. Confirm the PR title uses this repo's emoji convention (`:bug:`, `:sparkles:`, `:book:`,
   `:seedling:`, etc. — see [.github/pull_request_template.md](../../../.github/pull_request_template.md)).

## Design lean: favor vendor and platform-agnostic solutions (not a gate)

When the underlying need comes from an ACM/OCP/HCM Jira, the fastest path is usually to solve
it exactly as filed. Where it doesn't cost much extra thought or effort, lean toward a
solution that works for any OCM adopter running on any platform — not just Red Hat's specific
ACM/OpenShift setup.

In practice that usually means:

- Prefer a config option, feature gate, or extension point over behavior that's hardcoded to
  ACM's specific names, namespaces, defaults, or assumptions.
- Ask yourself: "would another company running OCM on their own platform get value from this,
  as designed?" If yes, you're in good shape. If the honest answer is "only Red Hat would ever
  use this," that's a signal the change might belong downstream-only rather than in the
  community repo at all — see [Scope](#scope-which-repos-this-applies-to).

Why this is worth the extra thought: a solution that's genuinely useful to the whole community
gets used, tested, and maintained by more than just Red Hat engineers, so it's far less likely
to bit-rot, get flagged as dead weight, or get reworked/reverted by maintainers later. It's
also an investment that compounds — the more of OCM stays truly multi-vendor, the more other
companies' contributions and fixes benefit ACM in return. A narrowly ACM-specific change that
lands upstream can quietly turn into tech debt that only Red Hat has the context to keep
alive, which is a worse long-term outcome than spending a bit more thought on the design now.

## The recommended workflow

Worked example: you're a Red Hatter picking up work tracked by an ACM, OCP, or HCM Jira
ticket, and the fix/feature needs to land in an OCM community repo.

1. Keep the internal ticket (e.g. `ACM-1234`) exactly where it is — that's your team's
   tracking, it's not part of the public workflow.
2. Classify the change as **trivial** or **non-trivial** (table below).
3. **Trivial** → open a sanitized, DCO-signed PR directly. Note: this repo's own
   [CONTRIBUTING.md](../../../CONTRIBUTING.md) formally asks for an issue before any patch —
   this guide treats that as the letter of the rule and gives you a narrower, pragmatic
   default: for small, self-evident fixes (typos, obvious bug fixes, dep bumps), skip the
   separate issue and go straight to the PR. No design review or Slack post needed either.
4. **Non-trivial** →
   1. Open a public GitHub issue in the target repo describing the problem/proposal
      (sanitized, per Rule #1). Link the GitHub issue URL into your internal Jira ticket for
      traceability.
   2. If it touches an API/CRD, a new component, or cross-repo design, write a short design
      note — the GitHub issue body is often enough — and get it in front of the right people:
      tag the repo's maintainers/approvers (see its `OWNERS` file) on the issue, and/or raise
      it at an OCM community/SIG meeting.
   3. If the change matches [Is this an enhancement?](#is-this-an-enhancement-proposal) below,
      file a formal proposal in `open-cluster-management-io/enhancements` instead of, or in
      addition to, the issue.
   4. Incorporate feedback on the issue/proposal. Wait for a maintainer to signal agreement
      with the direction (an approving comment, an `/approve`, or a triage label) before
      sinking significant implementation time into it.
   5. Implement the change and open the PR referencing the issue (`Fixes #NNN` /
      `Related to #NNN`), following the repo's normal review process.
   6. If the change is significant, post a short note with the issue/PR link in the community
      Slack (see [Community visibility](#community-visibility-when-and-how-to-post-in-slack))
      to get more eyes on it. Skip this for trivial changes.
   7. Address review feedback and merge.

## Trivial vs. non-trivial — quick heuristic

| Trivial — PR directly, no issue/Slack needed | Non-trivial — issue first, consider design review + Slack |
|---|---|
| Typo/doc fixes | New API fields/CRDs |
| Small, well-understood bug fix | New controller, feature, or addon |
| Dependency/version bump | Behavior change visible to existing users |
| Flaky test fix | Breaking change |
| Refactor with no behavior change | Security-relevant change |
| Added test coverage | Change spanning multiple repos/SIGs |
| | Anything you'd expect to see in a release note |

When a change straddles the line, default to filing the issue — it's cheap, and it gives
maintainers a heads-up even if you end up merging the PR the same day.

## Is this an enhancement proposal?

Quoted directly from `open-cluster-management-io/enhancements`'s own README — consider
creating an enhancement proposal there if your idea:

- Would be worth writing a blog post about after release
- Requires significant effort or introduces substantial changes
- Impacts upgrade or downgrade processes
- Requires coordination across multiple repositories or domains
- Introduces API changes or graduates between stability levels
- Will be noticed by users and become something they rely on

Per that same README, you probably don't need an enhancement proposal if your work fixes a
bug, adds more testing, refactors code that only affects internal implementation, or has
minimal impact on the project as a whole — a normal issue + PR is enough. If you're unsure,
open an issue and ask.

## Proposing a new repo

Only consider a brand-new repo when the work doesn't fit any existing OCM repo and is a
genuinely new, independently-releasable component (e.g. a new addon).

1. Socialize the idea first — an issue or an enhancement proposal in
   `open-cluster-management-io/enhancements` — and get SIG/maintainer buy-in before writing
   code.
2. Pick the right org once there's agreement: a community-facing OCM component meant for
   general adoption goes under `open-cluster-management-io`; Red Hat/ACM-specific tooling not
   meant for the broader community goes under `stolostron`.
3. Create the repo after that discussion, not before.

## Community visibility: when and how to post in Slack

Channel: https://kubernetes.slack.com/channels/open-cluster-mgmt

- **Post** when you've opened a non-trivial issue, PR, or enhancement proposal (per the table
  above) and want community awareness or extra reviewers.
- Keep it short: one or two lines plus the link, e.g. *"Opened an issue proposing X for
  cluster-proxy — feedback welcome: `<link>`"*.

## Non-goals

- This does not replace any repo's own `CONTRIBUTING.md`/DCO requirements — it's a Red
  Hat–specific overlay on top of them.
- This is not a gate. Never hold up a trivial fix waiting for a design review or a Slack
  thread.
- This does not apply outside the repos listed in [Scope](#scope-which-repos-this-applies-to).

## See also

For general OCM contribution info not specific to Red Hat (mailing list, community
meetings, the `kep`-style enhancement format) see the project's own guide:
https://open-cluster-management.io/docs/contribution-guidelines/
