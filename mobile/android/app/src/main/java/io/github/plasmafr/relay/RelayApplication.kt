package io.github.plasmafr.relay

import android.app.Application
import io.github.plasmafr.relay.data.RelayRepository

class RelayApplication : Application() {
    val repository: RelayRepository by lazy { RelayRepository(this) }
}
