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
        // Same forced-property pattern as DpadKeys: old TV WebViews ignore
        // KeyboardEvent init-dict fields, and the page matches on either
        // key or keyCode.
        "(function(){" +
            "var e=document.createEvent('KeyboardEvent');" +
            "e.initKeyboardEvent('$type',true,true,window,'XF86Back',0,'',false,'');" +
            "Object.defineProperty(e,'key',{get:function(){return 'XF86Back'}});" +
            "Object.defineProperty(e,'keyCode',{get:function(){return 10009}});" +
            "Object.defineProperty(e,'which',{get:function(){return 10009}});" +
            "document.dispatchEvent(e);})();"
}
