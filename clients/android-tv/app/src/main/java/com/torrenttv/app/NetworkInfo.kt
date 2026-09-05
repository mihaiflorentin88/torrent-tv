package com.torrenttv.app

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkAddress
import android.net.LinkProperties

/** Pure mask math so the discovery subnet derives testably from a prefix. */
object SubnetMask {
    fun forPrefix(prefixLength: Int): String {
        val prefix = prefixLength.coerceIn(0, 32)
        val mask = if (prefix == 0) 0 else (-1 shl (32 - prefix))
        return listOf(mask ushr 24 and 0xff, mask ushr 16 and 0xff, mask ushr 8 and 0xff, mask and 0xff)
            .joinToString(".")
    }
}

interface NetworkInfoSource {
    fun ip(): String?
    fun subnetMask(): String?
}

/**
 * What the Setup screen's LAN scan needs: this device's IPv4 address and
 * subnet mask, from the active network's link properties — the API-correct
 * source on both Wi-Fi and Ethernet for every supported platform (API 26+).
 * Nulls mean "unknown" (no active network yet, or no IPv4 address) and the
 * page degrades to manual address entry. The legacy WifiManager DHCP block
 * the plan proposed as a fallback is no longer public SDK surface (absent
 * from the android-35 jar), so there is no second source to try.
 */
class LinkNetworkInfo(context: Context) : NetworkInfoSource {
    private val connectivity = context.getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager

    override fun ip(): String? = ipv4Address()?.address?.hostAddress

    override fun subnetMask(): String? {
        val address = ipv4Address() ?: return null
        return SubnetMask.forPrefix(address.prefixLength)
    }

    private fun ipv4Address(): LinkAddress? {
        val properties: LinkProperties = connectivity.getLinkProperties(connectivity.activeNetwork) ?: return null
        return properties.linkAddresses.firstOrNull { it.address.address.size == 4 }
    }
}
