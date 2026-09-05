package com.torrenttv.app

import android.annotation.SuppressLint
import android.app.Activity
import android.graphics.Color
import android.os.Bundle
import android.util.Log
import android.view.KeyEvent
import android.view.WindowManager
import android.webkit.RenderProcessGoneDetail
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.webkit.WebViewAssetLoader
import androidx.webkit.WebViewClientCompat

class MainActivity : Activity() {
    private lateinit var webView: WebView

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        webView = WebView(this)
        // Transparent wherever the page is transparent: the video surface
        // (added with the player bridge) shows through the player shell
        // exactly like Tizen's AVPlay plane shows through the object element.
        webView.setBackgroundColor(Color.TRANSPARENT)
        webView.settings.javaScriptEnabled = true
        webView.settings.domStorageEnabled = true
        webView.settings.mediaPlaybackRequiresUserGesture = false
        // The page origin is https (appassets domain) and the LAN server is
        // http: without this, mixed-content blocking kills every API call.
        webView.settings.mixedContentMode = WebSettings.MIXED_CONTENT_ALWAYS_ALLOW
        val loader = WebViewAssetLoader.Builder()
            .addPathHandler("/assets/", WebViewAssetLoader.AssetsPathHandler(this))
            .build()
        webView.webViewClient = object : WebViewClientCompat() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest): WebResourceResponse? =
                loader.shouldInterceptRequest(request.url)

            override fun onPageFinished(view: WebView, url: String) {
                Log.i("TorrentTV", "ready")
            }

            // The client never fails silently (spec): a dead renderer
            // restarts the shell instead of leaving a black screen.
            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                Log.e("TorrentTV", "render process gone; restarting the shell")
                recreate()
                return true
            }
        }
        webView.addJavascriptInterface(Bridge(this, LinkNetworkInfo(this)), "FileListTVNative")
        setContentView(webView)
        webView.loadUrl("https://appassets.androidplatform.net/assets/www/index.html")
    }

    override fun onKeyDown(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            if (event?.repeatCount == 0) webView.evaluateJavascript(BackKeys.downScript(), null)
            return true
        }
        return super.onKeyDown(keyCode, event)
    }

    override fun onKeyUp(keyCode: Int, event: KeyEvent?): Boolean {
        if (keyCode == KeyEvent.KEYCODE_BACK) {
            webView.evaluateJavascript(BackKeys.upScript(), null)
            return true
        }
        return super.onKeyUp(keyCode, event)
    }
}
