# Build and release

The application uses the Wails v3 CLI pinned by the Go module:

```sh
go install github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.23
```

From the repository root, use `wails3 task build`, `wails3 task package`, `wails3 task run`, or `wails3 task dev` for the current desktop host. These tasks generate bindings, build the frontend, and compile the desktop application.

Linux builds require the native GTK4 and WebKitGTK development packages. The packaged Linux executable also uses the host's GTK4/WebKitGTK runtime.

## Android

Android builds require Linux or macOS, Android SDK API 35, build-tools, NDK `26.3.11579264`, JDK 17 or newer, npm, and Go 1.26.5 or newer. Set `ANDROID_HOME` and optionally `ANDROID_NDK_HOME`.

```sh
wails3 task android:toolchain:check
wails3 task android:build             # arm64 debug APK
wails3 task android:build ARCH=amd64  # x86_64 emulator APK
wails3 task android:package           # arm64 APK
```

The output is in `build/android/app/build/outputs/apk/{debug,release}/`. See the [Android build guide](android/README.md) for installation and storage details. Debug-signed APKs are not suitable for Google Play distribution.

## Release assets

Pushing a version tag beginning with `v` runs the application builds and prepares a draft GitHub Release containing:

- `WazzapAgent-windows-amd64.exe`
- `WazzapAgent-linux-amd64.tar.gz`
- `WazzapAgent-android-arm64-debug.apk`
- `SHA256SUMS`

The Android release artifact is debug-signed. Linux releases contain the application executable and require GTK4 and WebKitGTK on the target system.

## SQLite migrations

Migration files must use LF line endings. Their embedded bytes determine the checksums stored in existing databases; converting them to CRLF can cause an `applied migration checksum mismatch` at startup. `.gitattributes` enforces LF, and the application build workflow checks migration line endings.
