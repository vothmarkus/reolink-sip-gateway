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
        versionCode = 3
        versionName = "0.2.1-alpha3"
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
