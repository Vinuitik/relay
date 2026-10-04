package com.relay.app.data

import android.content.Context
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.floatPreferencesKey
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map

private val Context.appPrefsDataStore by preferencesDataStore(name = "app_prefs")

/**
 * Small UI preferences that should survive a restart (DESIGN.md "restore where the user was"):
 * - [currentRunner]: hostname Home shows (the runner switcher's selection).
 * - [lastRoute]: last Home/Project/Chat route, restored on a cold launch by RelayNavHost.
 * - [lastProvider]: provider the last "New chat" used, so the next one needs no dialog.
 * - [usageCosts]: the Usage screen's money assumptions (idle watts per runner, the rest global).
 * Separate DataStore file from [KnownRunnersRepository] so the runners blob stays untouched.
 */
class AppPrefsRepository(context: Context) {

    private val store = context.applicationContext.appPrefsDataStore

    private object Keys {
        val CURRENT_RUNNER = stringPreferencesKey("current_runner")
        val LAST_ROUTE = stringPreferencesKey("last_route")
        val LAST_PROVIDER = stringPreferencesKey("last_provider")
    }

    val currentRunner: Flow<String?> = store.data.map { it[Keys.CURRENT_RUNNER] }
    val lastRoute: Flow<String?> = store.data.map { it[Keys.LAST_ROUTE] }
    val lastProvider: Flow<String> = store.data.map { it[Keys.LAST_PROVIDER] ?: DEFAULT_PROVIDER }

    suspend fun setCurrentRunner(hostname: String) = store.edit { it[Keys.CURRENT_RUNNER] = hostname }
    suspend fun setLastRoute(route: String) = store.edit { it[Keys.LAST_ROUTE] = route }
    suspend fun setLastProvider(provider: String) = store.edit { it[Keys.LAST_PROVIDER] = provider }

    fun usageCosts(hostname: String): Flow<UsageCosts> = store.data.map { p ->
        val d = UsageCosts()
        UsageCosts(
            idleWatts = p[floatPreferencesKey("usage_idle_w_$hostname")] ?: d.idleWatts,
            sleepWatts = p[floatPreferencesKey("usage_sleep_w")] ?: d.sleepWatts,
            pricePerKwh = p[floatPreferencesKey("usage_price_kwh")] ?: d.pricePerKwh,
            deviceCost = p[floatPreferencesKey("usage_device_cost")] ?: d.deviceCost,
            deviceWatts = p[floatPreferencesKey("usage_device_w")] ?: d.deviceWatts,
        )
    }

    suspend fun setUsageCosts(hostname: String, c: UsageCosts) = store.edit {
        it[floatPreferencesKey("usage_idle_w_$hostname")] = c.idleWatts
        it[floatPreferencesKey("usage_sleep_w")] = c.sleepWatts
        it[floatPreferencesKey("usage_price_kwh")] = c.pricePerKwh
        it[floatPreferencesKey("usage_device_cost")] = c.deviceCost
        it[floatPreferencesKey("usage_device_w")] = c.deviceWatts
    }

    /**
     * Money assumptions for the Usage screen. Defaults: Dell laptop measured ~5 W idle with the
     * screen off and ~0.5 W asleep; £0.25/kWh; the £35.60 Pi board found; a Pi Zero-class
     * waker drawing ~1 W around the clock.
     */
    data class UsageCosts(
        val idleWatts: Float = 5f,
        val sleepWatts: Float = 0.5f,
        val pricePerKwh: Float = 0.25f,
        val deviceCost: Float = 35.6f,
        val deviceWatts: Float = 1f,
    )

    companion object {
        const val DEFAULT_PROVIDER = "claude"
        /** Providers offered on a long-press of "New chat". */
        val KNOWN_PROVIDERS = listOf("claude", "codex")
    }
}
