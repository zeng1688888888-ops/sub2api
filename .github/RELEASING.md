# Fork release process

This fork publishes only after CI and Security Scan succeed for the exact commit selected by Release. The release workflow calls both validation workflows; a manually dispatched Release cannot bypass these gates. All image architectures and archives come from that same commit.

1. Fetch upstream and merge the intended upstream changes into this fork's main branch. Resolve conflicts while preserving fork features.
2. Update backend/cmd/server/VERSION to a new, unused stable version. Keep the version history of this fork independent from upstream; the same version number does not imply identical code.
3. Commit and push main normally (without skip-ci markers), then create and push the corresponding annotated vX.Y.Z tag. Never move an already published tag or replace its image.
4. Check CI, Security Scan, and Release in Actions. Release repeats both checks against its selected SHA before building or publishing. Resolve failures in a new commit and version; do not disable the gates or add blanket vulnerability exceptions.
5. Confirm the GitHub Release assets and GHCR image tag/digest. A green Release means its embedded CI and Security Scan gates passed, even when it was started manually.
6. On the server, update only the application's image in the existing Compose file, pull that service, and recreate it with --no-deps. Keep the existing database, Redis, environment, and data mounts. Database migrations may run during application startup; retain a verified database backup before upgrading. Never use down -v or delete data directories during an update.

For a build-only rehearsal, manually run Release with dry_run enabled and an explicit ref. This still runs both validation gates, but does not publish images or a release.

Go versions are pinned in backend/go.mod and the three Dockerfiles. CI asserts the selected toolchain matches go.mod. Security Scan uses govulncheck plus production pnpm audit; malformed or network-error audit reports fail the gate. Existing scoped audit exceptions remain subject to their expiry dates.
