plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

android {
    namespace = "com.torrenttv.app"
    compileSdk = 35
    defaultConfig {
        applicationId = "com.torrenttv.app"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
    }
    buildTypes {
        named("release") {
            isMinifyEnabled = false
            // Sideloading artifact: the debug key keeps `adb install` and
            // manual updates working without a distribution certificate,
            // mirroring the deliberately unsigned Tizen WGT.
            signingConfig = signingConfigs.getByName("debug")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
}

dependencies {
    implementation("androidx.media3:media3-exoplayer:1.5.1")
    implementation("androidx.webkit:webkit:1.12.1")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}

// Sync the shared TV web app into WebView assets. index.html is the Android
// page variant (TorrentTV branding, no $WEBAPIS tag, platform-bridge.js);
// everything else — app.js, app.css, boot scripts — ships byte-identical to
// the Tizen WGT.
val syncWebApp = tasks.register<Copy>("syncWebApp") {
    from("../../tv/dist") { exclude("index.html") }
    from("assets/index.html")
    from("assets/platform-bridge.js")
    into("src/main/assets/www")
}
tasks.named("preBuild") { dependsOn(syncWebApp) }
