# Development

```bash
make run        # build and open the dashboard
make test       # run the tests
make check      # everything CI runs
make daemon     # run the background checks in the foreground (Ctrl+C stops)
make preview W=45 H=32          # print one frame at a size, with live data
make preview W=45 H=32 SCREEN=detail     # also help, docker, prune,
                                         # container:NAME, project:NAME, logs:NAME,
                                         # security[:ID], updates[:ID], services[:ID],
                                         # services:logs:UNIT, procs[:pid:N],
                                         # storage, network, agents, settings, explore:/
```

Builds from a checkout are development builds. They keep their own settings and audit log in `~/.config/opsie-dev` and `~/.local/state/opsie-dev`, say **dev** in the header, never update themselves, and never install the background service: `make daemon` runs it by hand, keeping its status in `/run/opsie-dev` where the dev dashboard looks, so working on opsie never touches the copy you installed from a release.

The tests render every screen at sizes from 30×10 up to 200×60 and fail if any line is wider or taller than the terminal, so the phone layout can't quietly break.

## Releasing

1. Write the release on GitHub with a new tag (`v0.2.0`). Put the changes in the notes as a bullet list; `opsie update` shows those bullets to everyone updating.
2. Publish it. The release workflow tests, builds the Linux binaries with GoReleaser, and attaches them with `checksums.txt`.
