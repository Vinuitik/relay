package com.relay.app.ui.screens

import android.Manifest
import android.content.pm.PackageManager
import android.net.Uri
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.Preview
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalLifecycleOwner
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.content.ContextCompat
import com.google.mlkit.vision.barcode.BarcodeScanning
import com.google.mlkit.vision.barcode.common.Barcode
import com.google.mlkit.vision.common.InputImage
import com.relay.app.model.KnownRunner
import java.util.concurrent.Executors

/**
 * Parsed result of scanning a runner's pairing QR code - see runner/FLOWS.md "Install
 * bootstrap" for where the QR content (`relay://host:port?key=...`) comes from
 * (`relay-runner -qr`, printed by install.sh).
 */
data class ScannedRunner(val hostname: String, val port: Int, val key: String)

/**
 * Parses the `relay://host:port?key=...` URI printed by `relay-runner -qr`. Returns null for
 * anything else scanned (a random QR code in the wild, an empty/malformed value) rather than
 * throwing - the caller shows a "not a Relay code" message instead of crashing on a bad scan.
 * Any extra query parameters the runner may emit (e.g. `mac=`) are ignored.
 */
fun parseRelayQrContent(raw: String): ScannedRunner? {
    val uri = runCatching { Uri.parse(raw) }.getOrNull() ?: return null
    if (uri.scheme != "relay") return null
    val hostname = uri.host?.takeIf { it.isNotBlank() } ?: return null
    val port = if (uri.port != -1) uri.port else KnownRunner.DEFAULT_PORT
    val key = uri.getQueryParameter("key")?.takeIf { it.isNotBlank() } ?: return null
    return ScannedRunner(hostname, port, key)
}

/**
 * Full-screen camera scanner for pairing a runner. Requests CAMERA permission on first show;
 * once granted, streams a CameraX preview through an ML Kit on-device barcode reader (no
 * network call - the decoded text stays on-device) and calls [onScanned] with the first frame
 * that parses as a `relay://` URI. [onManualEntry] switches to typing the key by hand (denied
 * permission, no camera, or just preference); [onClose] abandons adding a runner entirely.
 */
@Composable
fun QrScanScreen(
    onScanned: (ScannedRunner) -> Unit,
    onManualEntry: () -> Unit,
    onClose: () -> Unit,
) {
    val context = LocalContext.current
    var hasCameraPermission by remember {
        mutableStateOf(
            ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) ==
                PackageManager.PERMISSION_GRANTED,
        )
    }
    var permissionDenied by remember { mutableStateOf(false) }

    val permissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        hasCameraPermission = granted
        if (!granted) permissionDenied = true
    }

    // Ask immediately on first composition rather than a separate "enable camera" tap - scanning
    // is the entire purpose of this screen, so there's nothing useful to show without it.
    DisposableEffect(Unit) {
        if (!hasCameraPermission) permissionLauncher.launch(Manifest.permission.CAMERA)
        onDispose {}
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Scan runner QR code") },
                navigationIcon = { TextButton(onClick = onClose) { Text("Close") } },
            )
        },
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize(), contentAlignment = Alignment.Center) {
            when {
                hasCameraPermission -> Column(modifier = Modifier.fillMaxSize()) {
                    Box(modifier = Modifier.weight(1f)) {
                        CameraPreviewWithScanner(onScanned = onScanned)
                    }
                    TextButton(onClick = onManualEntry) { Text("Enter key manually instead") }
                }
                permissionDenied -> Column {
                    Text(
                        "Camera permission is needed to scan the QR code.",
                        modifier = Modifier.padding(16.dp),
                    )
                    Button(onClick = { permissionLauncher.launch(Manifest.permission.CAMERA) }) {
                        Text("Grant permission")
                    }
                    TextButton(onClick = onManualEntry) { Text("Enter key manually instead") }
                }
                else -> Text("Requesting camera permission…")
            }
        }
    }
}

@Composable
private fun CameraPreviewWithScanner(onScanned: (ScannedRunner) -> Unit) {
    val lifecycleOwner = LocalLifecycleOwner.current
    // Guards against firing onScanned multiple times for the same held-up QR code while frames
    // keep streaming in during the (fast, but non-zero) time it takes the caller to unmount this
    // screen after the first successful decode.
    var hasScanned by remember { mutableStateOf(false) }

    AndroidView(
        modifier = Modifier.fillMaxSize(),
        factory = { ctx ->
            val previewView = PreviewView(ctx)
            val cameraProviderFuture = ProcessCameraProvider.getInstance(ctx)
            val analysisExecutor = Executors.newSingleThreadExecutor()
            val scanner = BarcodeScanning.getClient()

            cameraProviderFuture.addListener({
                val cameraProvider = cameraProviderFuture.get()
                val preview = Preview.Builder().build().also {
                    it.setSurfaceProvider(previewView.surfaceProvider)
                }
                val analysis = ImageAnalysis.Builder()
                    .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                    .build()
                    .also { imageAnalysis ->
                        imageAnalysis.setAnalyzer(analysisExecutor) { proxy ->
                            val mediaImage = proxy.image
                            if (mediaImage == null || hasScanned) {
                                proxy.close()
                                return@setAnalyzer
                            }
                            val image = InputImage.fromMediaImage(mediaImage, proxy.imageInfo.rotationDegrees)
                            scanner.process(image)
                                .addOnSuccessListener { barcodes ->
                                    val parsed = barcodes.firstNotNullOfOrNull { barcode: Barcode ->
                                        barcode.rawValue?.let(::parseRelayQrContent)
                                    }
                                    if (parsed != null && !hasScanned) {
                                        hasScanned = true
                                        onScanned(parsed)
                                    }
                                }
                                .addOnCompleteListener { proxy.close() }
                        }
                    }
                cameraProvider.unbindAll()
                cameraProvider.bindToLifecycle(
                    lifecycleOwner,
                    CameraSelector.DEFAULT_BACK_CAMERA,
                    preview,
                    analysis,
                )
            }, ContextCompat.getMainExecutor(ctx))

            previewView
        },
    )
}
