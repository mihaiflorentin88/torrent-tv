package com.torrenttv.app

/**
 * Synthetic key events that forward the Android BACK key into the page as the
 * Tizen Return key (keyCode 10009), so the web app's long-press-to-exit and
 * back-stack behavior run unchanged. keydown starts the page's 5 s exit
 * timer; keyup inside the window fires the ordinary back action.
 */
object BackKeys {
    fun downScript(): String = keyScript("keydown")
    fun upScript(): String = keyScript("keyup")

    private fun keyScript(type: String): String =
        "(function(){document.dispatchEvent(new KeyboardEvent('$type'," +
            "{key:'XF86Back',keyCode:10009,which:10009,bubbles:true,cancelable:true}));})();"
}
