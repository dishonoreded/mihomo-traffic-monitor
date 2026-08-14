# Issue Tracker: GitHub

Issues and PRDs for this repository live in GitHub Issues. Use the `gh` CLI
from a clone of this repository so it can infer `dishonoreded/mihomo-traffic-monitor`.

## Conventions

- Create: `gh issue create --title "..." --body-file <file>`.
- Fetch: `gh issue view <number> --comments --json number,title,body,labels,comments,state,assignees`.
- List: `gh issue list --state open --json number,title,body,labels,comments,assignees`.
- Comment: `gh issue comment <number> --body "..."`.
- Apply or remove state labels with `gh issue edit --add-label` and `--remove-label`.
- Close: `gh issue close <number> --comment "..."`.

When a skill says to publish a spec, create a GitHub issue and apply
`ready-for-agent`. Pull requests are not a request or triage surface.

GitHub issues and pull requests share a number space. If an identifier is
ambiguous, try `gh pr view <number>` and then `gh issue view <number>`.

## Wayfinding Operations

- A map is one issue labelled `wayfinder:map`.
- Decision tickets are child issues labelled with one of
  `wayfinder:research`, `wayfinder:prototype`, `wayfinder:grilling`, or
  `wayfinder:task`.
- Prefer GitHub sub-issues and native issue dependencies. If unavailable, put
  children in the map's task list and add `Part of #<map>` and
  `Blocked by: #<issue>` lines to each child.
- The frontier is the map's ordered set of open children that have no open
  blocker and no assignee.
- Claim a frontier ticket with `gh issue edit <number> --add-assignee @me`.
- Resolve a ticket by recording the answer in a comment, closing the ticket,
  and linking that answer from the map's Decisions-so-far section.
