package com.relay.app

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import com.relay.app.data.KnownRunnersRepository
import com.relay.app.data.WidgetConfigRepository
import com.relay.app.ui.navigation.RelayNavHost
import com.relay.app.ui.theme.RelayTheme

class MainActivity : ComponentActivity() {

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val runnersRepository = KnownRunnersRepository(applicationContext)
        val widgetConfigRepository = WidgetConfigRepository(applicationContext)

        setContent {
            RelayTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    RelayNavHost(
                        runnersRepository = runnersRepository,
                        widgetConfigRepository = widgetConfigRepository,
                    )
                }
            }
        }
    }
}
