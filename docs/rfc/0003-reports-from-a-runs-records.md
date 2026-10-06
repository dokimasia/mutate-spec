---
rfc: 0003
title: Reports from a run's records
author: Roy Klopper <roy.klopper@stealthscale.io>
status: Draft
created: 2026-10-02
updated: 2026-10-06
discussion: none
supersedes: none
superseded-by: none
produces-adr: tbd
---

# RFC-0003: Reports from a run's records

## Summary

Every engine writes one record per target and nothing else. One converter,
`mutate-report`, turns any set of records into eight reports: the
dashboard's import, SARIF, GitHub annotations, a Markdown summary, GitLab's
Code Quality report, SonarQube's generic issues, the mutation-testing report
schema and a standalone HTML page. The converter attaches the run's
provenance, maps paths and columns to the units each consumer reads, and
writes the same bytes for the same input. A new format is written once, for
every language.

## Motivation

The dashboard has to place a run in a repository's history, and a record
does not state the repository, the revision, the branch or the CI run.

Each other consumer reads its own format, with its own units and limits:

| Consumer | Format | Constraints |
|---|---|---|
| GitHub code scanning | SARIF 2.1.0 | A private repository needs GitHub Code Security. At most 25,000 results per run, of which the top 5,000 are shown, and 10 MB per gzip-compressed file. Only the `primaryLocationLineHash` fingerprint is used |
| GitHub Actions annotations | Workflow commands on standard output | 10 warnings per step and 50 annotations per job are shown. Property values escape `%`, carriage return, line feed, `:` and `,` |
| GitHub job summary | Markdown | At most 1 MiB per step |
| GitLab merge request | Code Quality JSON | Shown in the merge request on the Free tier. Findings with identical fingerprints show as one. A finding has a line and no column |
| SonarQube | Generic issue import format | SonarQube files every SARIF issue under security. The generic format changed in 10.3 and is required from 10.8 |
| The mutation-testing-elements viewer and the Stryker Dashboard | The mutation-testing report schema | Every mutated file's full source is required. StrykerJS writes `schemaVersion` `"1.0"` |

Writing these in every engine would mean eight mappings in each of five or
more languages, each free to drift from the others.

## Detailed design

### Components

| Component | Responsibility | Where it is |
|---|---|---|
| The record | One run of one target, as the run protocol defines it, with three fields added | Written by each engine |
| The converter | Reads records and the checkout, and writes one report in one format | The `mutate-report` repository, a Go module `go.dokimi.dev/mutate-report` with a command and a package |
| The mappings | What each format contains, field by field | This RFC |
| The report corpus | Records, a source tree and the expected bytes of every format | `spec/reports/` in mutate-spec |

The converter is one Go program for every engine's records, because a
record is the same in every language. It imports only the standard library.
It is published as release binaries for Linux, macOS and Windows on amd64
and arm64, and runs with `go run`. The dashboard's ingestion imports the same
package, so a report exported from the dashboard has the same bytes as one
exported in CI.

### The command

```sh
mutate-report -format <format> [-out <file>] [flags] <record files or directories>
```

| Flag | Meaning |
|---|---|
| `-format` | One of `dashboard`, `sarif`, `github`, `markdown`, `gitlab`, `sonarqube`, `elements`, `html`. Required |
| `-out` | The file to write, standard output by default |
| `-repo-root` | The repository's root directory. By default the top level of the Git checkout around the current directory, or the current directory outside one |
| `-repository`, `-revision`, `-branch`, `-ci` | Override one provenance field |
| `-created-at` | The report's `provenance.createdAt`, the current time by default |
| `-thresholds high,low` | The thresholds of the `elements` and `html` reports, `80,60` by default, as in StrykerJS |

| Exit status | When |
|---|---|
| 0 | The report was written |
| 2 | An input, a flag or the checkout fails a check under Failure handling, and nothing was written |

A directory argument is read for every file whose name ends in
`.mutate.json`. The converter does not judge the run: an engine's own exit
status fails the CI job, and the converter succeeds for a report full of
survivors.

### The record's new fields

The record gains three fields:

| Field | Value |
|---|---|
| `startedAt` | When the run began, in UTC, in RFC 3339 form |
| `finishedAt` | When the run ended, in the same form |
| `root` | The absolute path of the target's module or project root at run time. Each mutant's `file` is relative to it |

`file` remains relative to the module root, so a key does not change when the
module moves within its repository. `root` lets the converter find the file
in the checkout.

### Provenance

The converter resolves each field once, from the first source that has it:

| Field | Flag | GitHub Actions | GitLab CI | Otherwise |
|---|---|---|---|---|
| `repository` | `-repository` | `$GITHUB_SERVER_URL/$GITHUB_REPOSITORY` | `$CI_PROJECT_URL` | `git remote get-url origin`, with any user name and password removed |
| `revision` | `-revision` | `git rev-parse HEAD` | `git rev-parse HEAD` | `git rev-parse HEAD` |
| `branch` | `-branch` | `$GITHUB_HEAD_REF`, then `$GITHUB_REF_NAME` | `$CI_MERGE_REQUEST_SOURCE_BRANCH_NAME`, then `$CI_COMMIT_REF_NAME` | `git branch --show-current` |
| `dirty` | none | `git status --porcelain` is not empty | the same | the same |
| `ci` | `-ci` | `$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID` | `$CI_JOB_URL` | absent |

The revision comes from the checkout in every case, because the checkout
is the code the engine ran. On a GitHub pull request that commit is the
merge commit, not the head of the branch. When no source supplies a field,
the report leaves it out, and the converter does not fail.

### Paths and columns

- **Paths.** A mutant's path in every report is `root` joined with `file`,
  made relative to the repository root, with `/` as the separator. A record
  whose root is outside the repository root fails the conversion.
- **The source must match.** Before it writes a report, the converter reads
  each listed mutant's span from the checked-out file, collapses its
  whitespace and cuts it with the mutant's `replacement` as the record cuts
  `original`, and compares the two. A file edited after the run fails the
  conversion.
- **Columns.** A record counts columns in bytes of UTF-8. Every format here
  except GitLab's counts UTF-16 code units, so the converter recounts each
  column from the file's line. SARIF declares `utf16CodeUnits`, its
  default. SonarQube's columns start at 0, and the others at 1.

### Which mutants each report lists

| Report | Mutants | Run errors |
|---|---|---|
| `dashboard`, `elements`, `html` | Every mutant of every record | In the records |
| `markdown` | Counts of every verdict, and the undetected mutants | Each one, per target |
| `sarif`, `github`, `gitlab`, `sonarqube` | The undetected ones: `survived` and `no-coverage` | Not reported |

Every report lists mutants by path, then line, then column, then kind, then
key.

### `dashboard`: the dashboard's import

```json
{
  "report": "dokimi-mutate-report",
  "version": 1,
  "provenance": {
    "repository": "https://github.com/acme/widgets",
    "revision": "9fceb02d0ae598e95dc970b74767f19372d61af8",
    "branch": "feature/limits",
    "dirty": false,
    "ci": "https://github.com/acme/widgets/actions/runs/4242",
    "createdAt": "2026-10-02T21:14:03Z"
  },
  "records": []
}
```

`records` contains each record unchanged, ordered by target name. The
report is lossless: every other report can be derived from it and the
checkout.

### `sarif`: SARIF 2.1.0

| Field | Value |
|---|---|
| `version` | `"2.1.0"` |
| `runs[]` | One run per engine name, with the mutants of that engine's records |
| `tool.driver` | `name` and `version` of the engine. `rules` has one entry per kind present: `id` is the kind, `shortDescription.text` the kind's description in the catalogue, `defaultConfiguration.level` is `warning` |
| `results[].ruleId` | The kind |
| `results[].level` | `warning` |
| `results[].message.text` | `Mutant survived: <original> became <replacement>, and no test failed.`, or for `no-coverage`, `No test executes <original>, so its mutant <replacement> never ran.` |
| `results[].locations[0]` | `physicalLocation.artifactLocation.uri` is the path, with `uriBaseId` `%SRCROOT%`. `region` contains the start and end line and column |
| `results[].partialFingerprints` | `primaryLocationLineHash` and `mutateKey/v1`, both the mutant's key |
| `results[].properties` | `verdict`, `key`, `scope`, `catalogue` and `overlay`, and `coveredBy` where the record states it |
| `versionControlProvenance[0]` | `repositoryUri`, `revisionId` and `branch` from the provenance |
| `properties.targets` | Each record's target name and score |

GitHub reads `primaryLocationLineHash` to match a result across runs, and
computes a hash of the line's text when it is absent. The key identifies
the same mutant across unrelated edits, which that line hash approximates,
so the converter supplies the key under both names.

### `github`: annotations

One workflow command per undetected mutant, on standard output:

```text
::warning file=<path>,line=<line>,endLine=<line>,col=<column>,endColumn=<column>,title=<kind> survived::<message>
```

The `title` for `no-coverage` reads `<kind> not covered`. The message is
the SARIF message. Property values escape `%`, carriage return, line feed,
`:` and `,`, and the message escapes the first three, as GitHub's toolkit
does. GitHub shows the first 10 warnings of a step, so a job that writes
annotations should also write the `markdown` report to
`$GITHUB_STEP_SUMMARY`.

### `markdown`: the summary

- A heading states the score of every record together, and the count it
  is taken over.
- A table gives one row per target: the score and the count of each
  verdict.
- Each run error is listed under its target.
- A table lists the undetected mutants: the path and line, the kind, and
  the original and the replacement.

The report stops below 1,000,000 bytes, under GitHub's 1 MiB limit for a
step. When the table does not fit, its last line states how many mutants it
left out.

### `gitlab`: Code Quality

A JSON array with one entry per undetected mutant:

| Field | Value |
|---|---|
| `description` | The SARIF message |
| `check_name` | `mutate.` followed by the kind |
| `fingerprint` | The key |
| `severity` | `major` |
| `location.path` | The path |
| `location.lines.begin` | The start line |

### `sonarqube`: generic issues

The format that SonarQube 10.3 and later read through
`sonar.externalIssuesReportPaths`.

| Field | Value |
|---|---|
| `rules[]` | One per kind present. `id` is `mutate.` followed by the kind, `name` and `description` come from the catalogue, `engineId` is `mutate`, `cleanCodeAttribute` is `TESTED`, `type` is `CODE_SMELL`, `severity` is `MAJOR`, and `impacts` is one impact on `MAINTAINABILITY` at `MEDIUM` |
| `issues[].ruleId` | The rule of the mutant's kind |
| `issues[].effortMinutes` | `10` |
| `issues[].primaryLocation` | `message` is the SARIF message, `filePath` the path, and `textRange` the start and end line and 0-based column |

`TESTED` is the clean code attribute that marks a gap in tests. The type,
severity and effort match the converter that the Stryker project publishes
for the same import.

### `elements`: the mutation-testing report schema

| Field | Value |
|---|---|
| `schemaVersion` | `"1.0"`, as StrykerJS writes |
| `thresholds` | From `-thresholds` |
| `framework` | The engine's `name` and `version` |
| `files.<path>` | `language` from the record, `source` the file's text, and `mutants` |
| `mutants[].id` | The key |
| `mutants[].mutatorName` | The kind |
| `mutants[].replacement` | The replacement |
| `mutants[].location` | The start and end line and column, both from 1 |
| `mutants[].status`, `statusReason` | From the verdict, in the table that follows |
| `mutants[].duration` | The run's seconds, in milliseconds |
| `mutants[].coveredBy` | The record's `coveredBy`, each test as its `id` in `testFiles` |
| `mutants[].killedBy` | The `tests` of a `killed` mutant, each test as its `id` in `testFiles` |
| `testFiles.<target>` | One entry per target whose tests a record names. `tests` lists each such test once, ordered by `id`: `id` is the target's name, a colon, a space and the test's name, and `name` is the test's name |

| Verdict | Status | Status reason |
|---|---|---|
| `killed` | `Killed` | The tests that failed |
| `timed-out` | `Timeout` | The tests that were running |
| `exhausted` | `Killed` | `exceeded the memory ceiling` |
| `survived` | `Survived` | none |
| `no-coverage` | `NoCoverage` | none |
| `not-viable` | `CompileError` | The toolchain's message |
| `suppressed` | `Ignored` | The rule or the annotation's reason |
| `not-selected` | `Ignored` | `not selected` |
| `not-run` | `Pending` | The cause |
| `error` | `RuntimeError` | The cause |

On a record without errors, the schema's mutation score then equals the
record's: it counts `Killed` and `Timeout` as detected, `Survived` and
`NoCoverage` as undetected, and leaves the rest out.

### `html`: a standalone page

One HTML file that contains the `elements` report and the viewer that
renders it, mutation-testing-elements 3.9.0's
`dist/mutation-test-elements.js`, 238,114 bytes, under the Apache 2.0
licence. The converter vendors that file at a pinned version, so a report
opens without a network connection.

### Determinism and the report corpus

The converter writes the same bytes for the same records, provenance and
checkout. Every list is ordered as stated. No report contains the time of
the conversion except `provenance.createdAt`, which `-created-at` sets.

`spec/reports/` in mutate-spec contains cases of records, a source tree, a
provenance and the expected bytes of every format. A converter passes when
it reproduces every file. The dashboard's export runs the same cases.

### Failure handling

| Failure | Result |
|---|---|
| A record of an unknown `record` name or `version` | Exit 2, naming the file |
| Two records for the same target | Exit 2 |
| Records of different catalogue major versions | Exit 2 |
| A record's root outside the repository root | Exit 2 |
| A source file missing, or its text not matching a mutant's `original` | Exit 2, naming the file and the mutant |
| The Markdown table over the size limit | Truncated, with the count it left out |
| More than 25,000 SARIF results in a run | Written. GitHub rejects the upload. A run restricted to the changed lines is far below the limit |

## Alternatives considered

### Every engine writes every format

**Why not:** eight mappings in five or more languages, each with its own
defects. The record is the same in every language, so one converter
covers them all.

### SARIF only

SARIF is an OASIS standard, and GitHub, Azure DevOps and the editors read
it.

**Why not:** SonarQube files a SARIF issue under security, where a
survivor would appear as a vulnerability. GitLab's merge request reads its
own Code Quality format. GitHub code scanning needs Code Security on a
private repository.

### Exports from the dashboard only

**Why not:** a CI job writes its reports where it runs, without a network
hop. A team without the dashboard gets them too. The dashboard imports the
converter's package, and its exports match.

### An HTML report of our own

**Why not:** the dashboard is where a viewer of our own belongs. The
mutation-testing-elements viewer renders the established cross-tool schema
today, under the Apache 2.0 licence.

### JUnit XML

Many CI systems render JUnit XML as a list of test results.

**Why not:** a mutant is not a test, and a test list shows it without its
file and line. GitHub, GitLab and SonarQube each read a format that places
the finding on its line.

### The mutation-testing report schema as the dashboard's import

**Why not:** it has no verdict for `exhausted`, `not-selected` or `not-run`,
no fields for the control runs, the limits or the inputs digest, and no
provenance. The record keeps all of these.

## Drawbacks

- **Mappings to formats defined elsewhere.** SonarQube changed its generic
  format in 10.3 and requires the new one from 10.8. GitHub's limits and
  StrykerJS's schema change on their own schedules.
- **A second CI step.** A job runs the engine and then the converter.
- **The HTML page includes a third-party bundle.** Each page is 238,114
  bytes larger, and a new viewer version is a converter release.
- **SARIF, the annotations, Code Quality and SonarQube are lossy.** They
  list only undetected mutants, and `elements` reports `exhausted` as
  `Killed`.
- **The checkout must match the run.** Columns and the `elements` source
  are read from the files, so a converter run on another checkout fails.
- **GitHub shows 10 annotations per step.** The rest are only in the
  summary and in SARIF.

## Open questions

- Which component sends the `dashboard` report to the platform, and how does
  it authenticate?

## Unresolved and future work

- Importing other tools' reports into the dashboard, such as PIT's XML or
  StrykerJS's JSON, is not proposed.
- Posting the Markdown summary as a pull request comment through a forge's
  API is not proposed. A CI job writes the file, and its forge integration
  decides where it goes.

## References

| What | Where |
|---|---|
| SARIF support for code scanning: required properties, `partialFingerprints`, limits, availability | https://docs.github.com/en/code-security/code-scanning/integrating-with-code-scanning/sarif-support-for-code-scanning |
| SARIF 2.1.0, OASIS Standard | https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html |
| Workflow commands: annotations, job summaries and their 1 MiB limit | https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands |
| GitHub Actions annotation limits per step and per job | https://github.com/actions/toolkit/blob/main/docs/problem-matchers.md |
| GitHub's escaping of workflow command values, `escapeData` and `escapeProperty` | https://github.com/actions/toolkit/blob/main/packages/core/src/command.ts |
| GitHub Actions variables: `GITHUB_SHA`, `GITHUB_REF_NAME`, `GITHUB_HEAD_REF`, `GITHUB_RUN_ID` | https://docs.github.com/en/actions/reference/workflows-and-actions/variables |
| GitLab Code Quality report format and tiers | https://docs.gitlab.com/ci/testing/code_quality/ |
| GitLab predefined CI/CD variables | https://docs.gitlab.com/ci/variables/predefined_variables/ |
| SonarQube generic issue import format | https://docs.sonarsource.com/sonarqube-server/latest/analyzing-source-code/importing-external-issues/generic-issue-import-format/ |
| SonarQube SARIF import, which assigns security to every issue | https://docs.sonarsource.com/sonarqube-server/latest/analyzing-source-code/importing-external-issues/importing-issues-from-sarif-reports/ |
| StrykerJS's converter to SonarQube's generic format: `CODE_SMELL`, `MAJOR`, 10 minutes, 0-based columns | https://github.com/stryker-mutator/mutation-testing-elements/blob/master/integrations/mutation-report-to-sonar.jq |
| The mutation-testing report schema, package version 3.9.0 | https://github.com/stryker-mutator/mutation-testing-elements/blob/master/packages/report-schema/src/mutation-testing-report-schema.json |
| StrykerJS writes `schemaVersion` `"1.0"` | https://github.com/stryker-mutator/stryker-js/blob/master/packages/core/src/reporters/mutation-test-report-helper.ts |
| mutation-testing-elements: the viewer and its use | https://github.com/stryker-mutator/mutation-testing-elements/tree/master/packages/elements |
| Stryker Dashboard: reports sent with an HTTP PUT | https://stryker-mutator.io/docs/General/dashboard/ |
