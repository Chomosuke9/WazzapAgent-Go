# Android P0 gate

Wails `v3.0.0-beta.23` builds Android as a native WebView app. The generated
project uses `build/android/gradlew`, a Java `MainActivity`/JNI bridge, and a
Go `libwails.so` compiled with `GOOS=android`, `CGO_ENABLED=1`, and
`-buildmode=c-shared`.

The required toolchain is SDK platform API 35, platform-tools, build-tools,
NDK `26.3.11579264`, JDK, npm, and Go 1.25 or newer. The beta.23 compile task
selects Darwin or Linux NDK toolchains; its source task does not support a
Windows host. Use `wails3 task android:toolchain:check` to inspect the local
setup.

The current repository has no generated native Android project yet, so
`wails3 task android:build` stops at that gate. No emulator/device operation is part
of this task file. In particular, beta.23's stock deploy tasks uninstall the
package before installing it; this project intentionally omits that behavior
so data persistence can be tested across upgrades.
