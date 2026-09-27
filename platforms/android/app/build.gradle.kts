plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

val orbVersion: String = (findProperty("orbVersion") as String?) ?: "0.0.0-dev"

android {
    namespace = "tech.ordalie.orb"
    compileSdk = 37
    defaultConfig {
        applicationId = "tech.ordalie.orb"
        minSdk = 29
        targetSdk = 36
        // The app is Orb: it carries the release version it was built for (-PorbVersion=0.13.0).
        versionName = orbVersion
        versionCode = orbVersion.substringBefore('-').split('.').mapNotNull { it.toIntOrNull() }.let { (it + listOf(0, 0, 0)).take(3) }
            .fold(0) { code, part -> code * 1000 + part }.coerceAtLeast(1)
        ndk { abiFilters += "arm64-v8a" }
    }
    // Development builds are signed with the release key when this machine has it, so they install
    // over a released app (and it over them) without losing its data. See release.sh.
    val release = file(System.getProperty("user.home") + "/.config/orb/android-release.jks")
    if (release.exists()) signingConfigs.create("orb") {
        storeFile = release
        storePassword = file(release.path.replace(".jks", ".pass")).readText().trim()
        keyAlias = "orb"
        keyPassword = storePassword
    }
    buildTypes.getByName("debug") { signingConfigs.findByName("orb")?.let { signingConfig = it } }
    buildFeatures { compose = true }
    // No fragments here: an old one only arrives transitively, so its activity-result check misfires.
    lint { disable += "InvalidFragmentVersionForActivityResult" }
    // The Orb core ships as an executable; Android only executes files extracted to nativeLibraryDir.
    packaging { jniLibs { useLegacyPackaging = true } }
    sourceSets.getByName("main").jniLibs.directories.add(layout.buildDirectory.dir("orb/jniLibs").get().asFile.path)
}

// The unmodified Orb CLI, cross-compiled pure Go (CGO_ENABLED=0), is the app's core.
val repo = rootDir.parentFile.parentFile
val orbCore = tasks.register<Exec>("orbCore") {
    val out = layout.buildDirectory.file("orb/jniLibs/arm64-v8a/liborb.so").get().asFile
    inputs.files(fileTree(repo) { include("**/*.go", "go.mod", "go.sum"); exclude("platforms/android/**", ".upstream/**") })
    outputs.file(out)
    workingDir = repo
    environment(mapOf("GOOS" to "android", "GOARCH" to "arm64", "CGO_ENABLED" to "0"))
    inputs.property("version", orbVersion)
    commandLine(System.getenv("GO") ?: "go", "build", "-trimpath", "-ldflags=-s -w -X main.version=$orbVersion", "-o", out.path, "./cmd/orb")
}
tasks.named("preBuild") { dependsOn(orbCore) }

dependencies {
    implementation(platform("androidx.compose:compose-bom:2026.09.00"))
    implementation("androidx.activity:activity-compose:1.13.0")
    implementation("androidx.compose.foundation:foundation")
    // Google's code scanner: Play services owns the camera, so the app needs no camera permission.
    implementation("com.google.android.gms:play-services-code-scanner:16.1.0")
    // Sign-in pages open in a Custom Tab: the owner's browser session, and back to the app when done.
    implementation("androidx.browser:browser:1.9.0")
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20250517") // android.jar's org.json is a stub on the JVM
}
