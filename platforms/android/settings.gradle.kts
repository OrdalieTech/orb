pluginManagement { repositories { google(); mavenCentral(); gradlePluginPortal() } }
dependencyResolutionManagement {
    repositories {
        google(); mavenCentral()
        // Termux's terminal view and emulator (Apache-2.0), built from its tagged sources; nothing else comes from here.
        maven("https://jitpack.io") { content { includeGroup("com.github.termux.termux-app") } }
    }
}
rootProject.name = "orb-android"
include(":app")
