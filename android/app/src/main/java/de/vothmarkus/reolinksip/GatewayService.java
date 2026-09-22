package de.vothmarkus.reolinksip;

import android.app.*;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.net.wifi.WifiManager;
import android.os.Build;
import android.os.IBinder;
import android.os.PowerManager;
import java.io.File;
import java.util.concurrent.*;
import de.vothmarkus.reolinksip.core.mobilebridge.Gateway;
import de.vothmarkus.reolinksip.core.mobilebridge.Listener;
import de.vothmarkus.reolinksip.core.mobilebridge.Mobilebridge;

public final class GatewayService extends Service {
    private static final String ACTION_RESTART = "de.vothmarkus.reolinksip.RESTART";
    private static final String CHANNEL = "gateway";
    private static final int NOTIFICATION_ID = 4102;
    private static volatile String lastState = "Gestoppt", lastStatus = "", lastError = "";
    private static volatile Gateway activeGateway;
    private final ScheduledExecutorService worker = Executors.newSingleThreadScheduledExecutor();
    private Gateway gateway;
    private MediaCodecAudioAdapter audioAdapter;
    private ScheduledFuture<?> networkRestart;
    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;
    private ConnectivityManager connectivity;
    private ConnectivityManager.NetworkCallback networkCallback;
    private String networkSignature = "";
    private volatile boolean destroyed;

    static void start(Context c) { c.startForegroundService(new Intent(c, GatewayService.class)); }
    static void restart(Context c) { c.startForegroundService(new Intent(c, GatewayService.class).setAction(ACTION_RESTART)); }
    static void stop(Context c) { c.stopService(new Intent(c, GatewayService.class)); }
    static String summary() { return GatewayStatus.summary(lastState, lastStatus, lastError); }
    static String rawStatus() { return lastStatus; }
    static boolean testAvailable() { return activeGateway != null && GatewayStatus.available(lastStatus, "test_call_available"); }
    static boolean hangupAvailable() { return activeGateway != null && GatewayStatus.available(lastStatus, "hangup_available"); }
    static void testCall() throws Exception {
        Gateway g = activeGateway;
        if (g == null) throw new IllegalStateException("Gateway läuft nicht");
        g.testCall("default");
    }
    static void hangup() throws Exception {
        Gateway g = activeGateway;
        if (g == null) throw new IllegalStateException("Gateway läuft nicht");
        g.hangup();
    }

    @Override public void onCreate() {
        super.onCreate();
        getSystemService(NotificationManager.class).createNotificationChannel(
                new NotificationChannel(CHANNEL, "Reolink SIP Gateway", NotificationManager.IMPORTANCE_LOW));
        Notification n = notification("Gateway wird gestartet");
        if (Build.VERSION.SDK_INT >= 29) startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE);
        else startForeground(NOTIFICATION_ID, n);
        // Blocking Go startup/stop and API calls never run on Android's main thread.
        worker.scheduleWithFixedDelay(this::poll, 1, 2, TimeUnit.SECONDS);
        watchNetwork();
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        boolean restart = intent != null && ACTION_RESTART.equals(intent.getAction());
        worker.execute(() -> {
            if (destroyed) return;
            try {
                if (restart) stopGateway();
                if (gateway == null) startGateway();
            } catch (Exception e) { failure(e); }
        });
        return START_STICKY;
    }

    private void startGateway() throws Exception {
        if (destroyed) return;
        String config = ConfigStore.buildJson(this);
        Mobilebridge.validateConfig(config);
        lastError = "";
        lastStatus = "";
        lastState = "Gateway startet";
        MediaCodecAudioAdapter adapter = new MediaCodecAudioAdapter();
        try {
            acquireLocks();
            Gateway next = Mobilebridge.start(config, new File(getFilesDir(), "gateway").getAbsolutePath(), adapter, new Listener() {
                @Override public void onState(String state) {
                    if (!destroyed) lastState = state.equals("stopped") ? "Gateway beendet" : "Gateway startet";
                }
                @Override public void onError(String error) { if (!destroyed) lastError = ConfigStore.redact(GatewayService.this, error); }
            });
            gateway = next;
            audioAdapter = adapter;
            activeGateway = next;
            lastState = "Gateway läuft";
            updateNotification(lastState);
        } catch (Exception e) {
            adapter.closeAll();
            releaseLocks();
            throw e;
        }
    }

    private void poll() {
        if (destroyed || gateway == null) return;
        try {
            if (!gateway.isRunning()) {
                String error = gateway.lastError();
                stopGateway();
                lastState = "Gateway beendet";
                lastError = error.isEmpty() ? "Bitte Einstellungen prüfen und erneut starten" : error;
                updateNotification(lastState);
                return;
            }
            lastStatus = gateway.statusJSON();
            lastError = gateway.lastError();
            String text = testAvailable() ? "Bereit für Anrufe" : "Gateway aktiv – Status in der App";
            updateNotification(text);
        } catch (Exception e) { lastError = message(e); }
    }

    private void stopGateway() throws Exception {
        Gateway old = gateway;
        if (activeGateway == old) activeGateway = null;
        lastStatus = "";
        if (old != null) {
            // Retain the object and decoder on timeout: a subsequent restart must
            // wait for the same runtime instead of racing its audio teardown.
            old.stop();
            gateway = null;
        }
        if (audioAdapter != null) { audioAdapter.closeAll(); audioAdapter = null; }
        releaseLocks();
    }

    private void failure(Exception e) {
        lastError = ConfigStore.redact(this, message(e));
        lastState = "Gateway-Fehler";
        updateNotification(lastState);
    }
    private static String message(Exception e) { return e.getMessage() == null ? e.toString() : e.getMessage(); }

    private void acquireLocks() {
        if (wakeLock == null) {
            wakeLock = getSystemService(PowerManager.class).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "reolinksip:gateway");
            wakeLock.setReferenceCounted(false);
        }
        if (!wakeLock.isHeld()) wakeLock.acquire();
        WifiManager wifi = (WifiManager) getApplicationContext().getSystemService(WIFI_SERVICE);
        if (wifi != null && wifiLock == null) {
            wifiLock = wifi.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "reolinksip:gateway");
            wifiLock.setReferenceCounted(false);
        }
        if (wifiLock != null && !wifiLock.isHeld()) wifiLock.acquire();
    }
    private void releaseLocks() {
        if (wifiLock != null && wifiLock.isHeld()) wifiLock.release();
        if (wakeLock != null && wakeLock.isHeld()) wakeLock.release();
    }

    private void watchNetwork() {
        connectivity = getSystemService(ConnectivityManager.class);
        if (connectivity == null) return;
        Network active = connectivity.getActiveNetwork();
        networkSignature = signature(active, active == null ? null : connectivity.getLinkProperties(active));
        networkCallback = new ConnectivityManager.NetworkCallback() {
            @Override public void onLinkPropertiesChanged(Network network, LinkProperties properties) {
                String next = signature(network, properties);
                if (destroyed) return;
                try {
                    worker.execute(() -> {
                        if (destroyed || next.equals(networkSignature)) return;
                        networkSignature = next;
                        if (gateway == null) return;
                        if (networkRestart != null) networkRestart.cancel(false);
                        networkRestart = worker.schedule(() -> {
                            if (destroyed || gateway == null) return;
                            try { lastState = "Netzwerk geändert – Neustart"; stopGateway(); startGateway(); }
                            catch (Exception e) { failure(e); }
                        }, 2, TimeUnit.SECONDS);
                    });
                } catch (RejectedExecutionException ignored) {}
            }
        };
        connectivity.registerDefaultNetworkCallback(networkCallback);
    }
    private static String signature(Network n, LinkProperties p) {
        if (n == null) return "";
        java.util.List<String> addresses = new java.util.ArrayList<>();
        if (p != null) for (android.net.LinkAddress address : p.getLinkAddresses())
            if (address.getAddress() instanceof java.net.Inet4Address) addresses.add(address.toString());
        java.util.Collections.sort(addresses);
        return n.toString() + ":" + addresses;
    }

    @Override public void onDestroy() {
        destroyed = true;
        if (connectivity != null && networkCallback != null) connectivity.unregisterNetworkCallback(networkCallback);
        worker.execute(() -> {
            try { stopGateway(); }
            catch (Exception e) { lastError = message(e); }
            finally {
                releaseLocks();
                lastState = "Gestoppt";
                lastStatus = "";
            }
        });
        worker.shutdown();
        stopForeground(STOP_FOREGROUND_REMOVE);
        super.onDestroy();
    }
    @Override public IBinder onBind(Intent intent) { return null; }
    private Notification notification(String text) {
        PendingIntent open = PendingIntent.getActivity(this, 0, new Intent(this, MainActivity.class), PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this, CHANNEL).setSmallIcon(android.R.drawable.stat_sys_phone_call)
                .setContentTitle("Reolink SIP Gateway").setContentText(text).setContentIntent(open).setOngoing(true).build();
    }
    private void updateNotification(String text) {
        if (!destroyed) getSystemService(NotificationManager.class).notify(NOTIFICATION_ID, notification(text));
    }
}
