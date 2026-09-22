plugins {
    id("com.android.application")
}

android {
    namespace = "de.vothmarkus.reolinksip"
    compileSdk = 36

    defaultConfig {
        applicationId = "de.vothmarkus.reolinksip"
        minSdk = 26
        targetSdk = 36
        versionCode = 1
        versionName = "0.1.0-alpha1"
    }

    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

dependencies {
    implementation(files("libs/reolink-core.aar"))
}
