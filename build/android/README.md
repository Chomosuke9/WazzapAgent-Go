# Android build

Wails `v3.0.0-beta.23` builds Android as a native WebView app. The generated
project uses `build/android/gradlew`, a Java `MainActivity`/JNI bridge, and a
Go `libwails.so` compiled with `GOOS=android`, `CGO_ENABLED=1`, and
`-buildmode=c-shared`.

The native Gradle project was generated from the pinned Wails CLI. The Go app
uses the same React bundle, Wails service bindings, controllers, settings, and
WhatsApp runtime as the desktop app. It stores data beneath Android's private
`getFilesDir()` directory. Android's activity lifecycle can stop the process;
the Agent is not yet a persistent background service.

Build on Linux or macOS with SDK API 35, build-tools, NDK
`26.3.11579264`, JDK 17 or newer, npm, and Go 1.26.5 or newer. WSL needs its own Linux
SDK/NDK installation. Set `ANDROID_HOME` and optionally `ANDROID_NDK_HOME`.
The pinned Wails compiler task does not select a Windows NDK toolchain.

```sh
wails3 task android:toolchain:check
wails3 task android:build             # arm64 debug APK
wails3 task android:build ARCH=amd64  # x86_64 emulator APK
wails3 task android:package           # arm64 release APK signed for local testing
```

The output is in `build/android/app/build/outputs/apk/{debug,release}/`.
For an existing installation, use `adb install -r <apk>` and launch the app;
do not uninstall it, because uninstalling removes the app's stored session.
The release task uses Android's debug signing key unless a release keystore is
provided with the `ANDROID_KEYSTORE_*` variables. The arm64 debug APK build
completed on a Debian x86_64 host with the isolated SDK/NDK toolchain. The APK
contains `libwails.so`, the expected package ID, and a valid debug signature.
A device smoke test and a sustained background run have not been completed yet.
