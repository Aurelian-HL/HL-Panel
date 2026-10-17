# Reliable UDP engine release recipe

This is a **build-time** source recipe, not a node install script. It produces
one Linux amd64 GOST binary from the reviewed timeout candidate. It does not
start engines, modify kernel settings, register nodes, or enable UDP rules.

`lock.json` pins the upstream GOST commit/archive, complete source trees, Go
1.27.0, authenticated x/smux module archives, the exact 13 final patched source
files, and the dependency graph. Go checksum database verification stays on.
The patch files are final source replacements from the accepted research
candidate; they contain no contract test files. Relative module replacements
and `-trimpath` avoid embedding the build directory in the binary.

## Build and inspect

From the repository root, on Windows or Linux with Go 1.27.0 and Python 3.10+:

```text
python deploy/reliable_udp_engine.py /absolute/new-empty-output
```

If the default module mirror cannot be reached, a single HTTPS origin may be
supplied with `--module-proxy https://goproxy.cn`. This changes only the mirror;
it does not disable the checksum database or any locked hash. All caches and
logs stay inside the output directory. Failed output is kept for diagnosis.

The result is `OUTPUT/bundle/`, with `gost`, upstream/module license files,
normalized Go build information, and a manifest. The command prints the manifest
and binary SHA256 values. Only Linux amd64 is supported. A Windows host can
cross-compile the ELF but cannot establish the Linux runtime acceptance result.

## Release integration

The default `fetch-node-engines.py RELEASE` behavior remains pinned upstream
engines. A reviewed bundle must be explicitly selected with both arguments:

```text
python deploy/fetch-node-engines.py RELEASE --gost-bundle OUTPUT/bundle --gost-manifest-sha256 REVIEWED_MANIFEST_SHA256
```

The importer checks the trusted recipe, pinned manifest, ELF architecture, file
hashes, module replacements and dependency versions before downloading or
writing release files. No silent fallback to upstream occurs if the candidate
is invalid. License/provenance files accompany the installed engine. Node
installers still copy a packaged binary; they do not download source or compile.

The PowerShell package builder accepts an explicit candidate without changing
its default engine selection:

```powershell
./deploy/build-release.ps1 -ReleaseDirectory RELEASE -Version CANDIDATE_VERSION -GostBundleDirectory OUTPUT/bundle -GostManifestSha256 REVIEWED_MANIFEST_SHA256
```

Both candidate arguments are required together. Invalid input fails closed;
there is no fallback to upstream. The recipe and source/build caches are not
copied into the node package. The old standalone official fetch helper needs
no recipe module. The isolated UDP workflow builds the real web and release
package, installs it through the token installer, and checks that packaged and
installed GOST binaries retain the accepted SHA256 and exact runtime identity
through update, rollback and reinstall. This is package acceptance, not panel
tunnel integration or production cutover.

## Required acceptance

The historical research binary (`5564ca4a...`) passed isolated timeout gates;
those results are not acceptance of a newly built release binary. Before
publishing a bundle, run on an isolated Linux runner:

- Recipe/importer contracts: `python tests/install/test_reliable_udp_engine.py`.
- Verify `go version -m OUTPUT/bundle/gost`, `OUTPUT/bundle/gost -V`, manifest
  hashes and actual binary SHA256 in retained CI output.
- Existing GOST/agent integration suites with `NYVP_GOST_BINARY` set to that
  exact binary, plus the reliable UDP authentication, bidirectional checksum,
  half-close, lossy close and old-flow timeout gates.
- Rule application, stable customer endpoint, rollback and connection continuity
  checks for the actual product integration.

The manifest deliberately records `engine_acceptance_run: false`: successful
compilation alone does not claim any of these runtime checks or production
readiness. WAN loss/latency, CPU and long-duration tests remain separate gates.
