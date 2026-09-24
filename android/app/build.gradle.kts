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
        versionCode = 6
        versionName = "0.3.2-alpha6"
    }

    signingConfigs {
        getByName("debug") {
            // CI creates/restores this exact file; do not rely on AGP's
            // machine-dependent default Android user-directory location.
            System.getenv("REOLINK_ANDROID_KEYSTORE")?.let { storeFile = file(it) }
        }
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
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}
