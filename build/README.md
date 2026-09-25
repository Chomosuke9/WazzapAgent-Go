# Build P0

The task files target the official Wails `v3.0.0-beta.23` source pinned by the
application module. Bootstrap the matching CLI with
`go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23`
and put the Go binary directory on PATH. After that,
`wails3 task common:wails:install` can reinstall the pinned CLI.
The Go entrypoint is `./cmd/app`, the product name is
`WazzapAgent`, and the application identifier is
`io.github.chomosuke9.wazzapagent`.

`wails3 task build`, `wails3 task package`, `wails3 task run`, and
`wails3 task dev` select the current
desktop host. The desktop tasks generate bindings, build `frontend/dist`, and
compile the GUI with the project `gui` tag. Windows is configured for a
no-cgo executable; Linux and macOS require their native Wails WebView toolchain
on the host that runs the task.

For P0, `package` produces a production executable only: it does not yet create
an installer, macOS `.app` bundle, signed package, or auto-update distribution.
Linux and macOS tasks are scaffolding and have not been run on those hosts.

The Android native Gradle project is generated from beta.23 and the app's
private-storage path is wired in. `wails3 task android:build` produces a debug
APK on a Linux or macOS host with Android SDK/NDK and JDK installed;
`android:package` produces a release APK for local testing. These tasks do not
install or uninstall the app. See [Android build](android/README.md) for
prerequisites and remaining device-validation work.

The common tasks keep binding generation separate from the frontend build:
`common:generate:bindings` or `common:generate:bindings:android` is followed
by `common:build:frontend`. `NPM` can override the npm command, for example
`wails3 task NPM=path/to/npm build`; no machine-specific path is committed.

GitHub Actions builds the Windows amd64 executable, Linux amd64 executable,
and Android arm64 debug APK on every push and pull request. Each run uploads
the three outputs as separate downloadable artifacts. It can also be started
manually with **Run workflow**. The Android artifact is a debug-signed APK for
testing, not a Play Store release.

Pushing a version tag beginning with `v` also creates a draft GitHub Release
after all three application builds succeed. The draft contains a Windows
executable, a Linux `.tar.gz`, the Android debug APK, and `SHA256SUMS`. The
separate `ci` workflow still needs to be checked before publishing. Review the
draft under **Releases**, then click **Publish release** when it is ready.

Create and push a tag from the commit to release:

```sh
git tag -a v0.1.0 -m "WazzapAgent v0.1.0"
git push origin v0.1.0
```

The Linux build is a raw executable and still needs the host's GTK4/WebKitGTK
runtime. The Android APK is debug-signed for testing; the release workflow does
not produce a Play Store package or claim stable-release readiness.
