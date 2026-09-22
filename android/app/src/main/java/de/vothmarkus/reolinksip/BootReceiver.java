package de.vothmarkus.reolinksip;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

public final class BootReceiver extends BroadcastReceiver {
    @Override
    public void onReceive(Context context, Intent intent) {
        String action = intent == null ? "" : intent.getAction();
        if ((Intent.ACTION_BOOT_COMPLETED.equals(action)
                || Intent.ACTION_MY_PACKAGE_REPLACED.equals(action))
                && ConfigStore.getBool(context, "start_on_boot", false)) {
            try {
                GatewayService.start(context);
            } catch (IllegalStateException | SecurityException e) {
                // Some device policies prohibit background starts. Opening the
                // app still permits a normal user-initiated foreground start.
                android.util.Log.w("ReolinkGateway", "Automatic start unavailable; open the app", e);
            }
        }
    }
}
