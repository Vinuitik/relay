package com.relay.app.network

import com.relay.app.model.KnownRunner
import com.squareup.moshi.Moshi
import com.squareup.moshi.kotlin.reflect.KotlinJsonAdapterFactory
import okhttp3.Interceptor
import okhttp3.OkHttpClient
import okhttp3.logging.HttpLoggingInterceptor
import retrofit2.Retrofit
import retrofit2.converter.moshi.MoshiConverterFactory

/**
 * Builds (and caches) a [RelayApiService] per known runner. Every request carries the
 * `X-Relay-Key` header from that runner's stored key (see ARCHITECTURE.md "Registration") — auth
 * is entirely this header plus the Tailscale tunnel, no OAuth/session state.
 *
 * Plain `http://` per the task's networking spec; ARCHITECTURE.md describes the tailnet as the
 * real transport security boundary (TLS termination is explicitly out of scope for v1).
 */
object RelayApiClient {

    private val cache = mutableMapOf<String, RelayApiService>()

    fun forRunner(runner: KnownRunner): RelayApiService {
        val cacheKey = "${runner.hostname}:${runner.port}:${runner.key}"
        return cache.getOrPut(cacheKey) { build(runner) }
    }

    private fun build(runner: KnownRunner): RelayApiService {
        val authInterceptor = Interceptor { chain ->
            val authed = chain.request().newBuilder()
                .addHeader("X-Relay-Key", runner.key)
                .build()
            chain.proceed(authed)
        }
        val logging = HttpLoggingInterceptor().apply {
            level = HttpLoggingInterceptor.Level.BASIC
        }
        val client = OkHttpClient.Builder()
            .addInterceptor(authInterceptor)
            .addInterceptor(logging)
            .build()
        val moshi = Moshi.Builder().add(KotlinJsonAdapterFactory()).build()

        return Retrofit.Builder()
            .baseUrl("http://${runner.hostname}:${runner.port}/")
            .client(client)
            .addConverterFactory(MoshiConverterFactory.create(moshi))
            .build()
            .create(RelayApiService::class.java)
    }
}
