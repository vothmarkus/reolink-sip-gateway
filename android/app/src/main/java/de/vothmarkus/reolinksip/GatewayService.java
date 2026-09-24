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
import android.os.SystemClock;
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
    private static volatile long lastStatusAt;
    // Shared across Service instances: Android can create the next instance
    // before the previous asynchronous native/audio teardown has completed.
    private static final ScheduledExecutorService worker = Executors.newSingleThreadScheduledExecutor();
    private static volatile GatewayService currentService;
    private static volatile String phase = "stopped";
    private ScheduledFuture<?> polling;
    private Gateway gateway;
    private MediaCodecAudioAdapter audioAdapter;
    private ScheduledFuture<?> networkRestart;
    private PowerManager.WakeLock wakeLock;
    private WifiManager.WifiLock wifiLock;
    private ConnectivityManager connectivity;
    private ConnectivityManager.NetworkCallback networkCallback;
    private String networkSignature = "";
    private volatile boolean destroyed;

    static void start(Context c) { requestStart(c, false); }
    static void restart(Context c) { requestStart(c, true); }
    private static void requestStart(Context c, boolean restart) {
        boolean previous = ConfigStore.getBool(c, "gateway_requested", false);
        ConfigStore.requestRun(c, true);
        try {
            c.startForegroundService(new Intent(c, GatewayService.class).setAction(restart ? ACTION_RESTART : null));
        } catch (RuntimeException e) { ConfigStore.requestRun(c, previous); throw e; }
    }
    static void stop(Context c) {
        ConfigStore.requestRun(c, false);
        phase = "stopping";
        if (!c.stopService(new Intent(c, GatewayService.class))) { phase = "stopped"; lastState = "Gestoppt"; }
    }
    // A failed foreground service remains switchable OFF. Showing an unchecked
    // switch on failure would prevent clearing its saved autostart intent.
    static boolean enabled() { return phase.equals("starting") || phase.equals("running") || phase.equals("failed"); }
    static boolean busy() { return phase.equals("starting") || phase.equals("stopping"); }
    static String phaseLabel() {
        switch (phase) {
            case "starting": return "Gateway startet …";
            case "running": return "Gateway eingeschaltet";
            case "stopping": return "Gateway wird beendet …";
            case "failed": return "Start oder Betrieb fehlgeschlagen";
            default: return "Gateway ausgeschaltet";
        }
    }
    static String error() { return lastError; }
    static String summary() {
        String summary = GatewayStatus.summary(lastState, lastStatus, lastError);
        return activeGateway == null ? summary : summary + "\n" + GatewayStatus.pollAge(lastStatusAt, SystemClock.elapsedRealtime());
    }
    static byte[] snapshotJPEG() throws Exception {
        Gateway current = activeGateway;
        if (current == null) throw new IllegalStateException("Gateway ist gestoppt");
        return current.snapshotJPEG();
    }
    static String liveImageURL() throws Exception {
        Gateway current = activeGateway;
        if (current == null) throw new IllegalStateException("Gateway ist gestoppt");
        return current.liveImageURL();
    }
    static String rawStatus() { return lastStatus; }
    static boolean testAvailable() { return activeGateway != null && GatewayStatus.available(lastStatus, "test_call_available"); }
    static boolean hangupAvailable() { return activeGateway != null && GatewayStatus.available(lastStatus, "hangup_available"); }
    static void testCall(String routeID) throws Exception {
        Gateway g = activeGateway;
        if (g == null) throw new IllegalStateException("Gateway läuft nicht");
        g.testCall(routeID);
    }
    static void hangup() throws Exception {
        Gateway g = activeGateway;
        if (g == null) throw new IllegalStateException("Gateway läuft nicht");
        g.hangup();
    }

    @Override public void onCreate() {
        super.onCreate();
        currentService = this;
        phase = "starting";
        getSystemService(NotificationManager.class).createNotificationChannel(
                new NotificationChannel(CHANNEL, "Reolink SIP Gateway", NotificationManager.IMPORTANCE_LOW));
        Notification n = notification("Gateway wird gestartet");
        if (Build.VERSION.SDK_INT >= 29) startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE);
        else startForeground(NOTIFICATION_ID, n);
        // Blocking Go startup/stop and API calls never run on Android's main thread.
        polling = worker.scheduleWithFixedDelay(this::poll, 1, 2, TimeUnit.SECONDS);
        watchNetwork();
    }

    @Override public int onStartCommand(Intent intent, int flags, int startId) {
        // A delayed/sticky start must not undo a subsequent explicit switch-off.
        if (!ConfigStore.getBool(this, "gateway_requested", true)) {
            stopSelf(startId);
            return START_NOT_STICKY;
        }
        boolean restart = (intent != null && ACTION_RESTART.equals(intent.getAction())) || phase.equals("failed");
        phase = "starting";
        worker.execute(() -> {
            if (destroyed) return;
            try {
                if (restart) stopGateway();
                if (gateway == null) startGateway();
                else if (!destroyed) phase = "running";
            } catch (Exception e) { failure(e); }
        });
        return START_STICKY;
    }

    private void startGateway() throws Exception {
        if (destroyed) return;
        phase = "starting";
        String config = ConfigStore.buildJson(this);
        Mobilebridge.validateConfig(config);
        lastError = "";
        lastStatus = "";
        lastStatusAt = 0;
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
            if (destroyed) return; // Queued teardown owns this instance now.
            phase = "running";
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
                if (destroyed) return;
                phase = "failed";
                lastState = "Gateway beendet";
                lastError = error.isEmpty() ? "Bitte Einstellungen prüfen und erneut starten" : error;
                updateNotification(lastState);
                return;
            }
            String snapshot = gateway.statusJSON();
            if (destroyed) return;
            lastStatus = snapshot;
            lastStatusAt = SystemClock.elapsedRealtime();
            lastError = gateway.lastError();
            String text = testAvailable() ? "Bereit für Anrufe" : "Gateway aktiv – Status in der App";
            updateNotification(text);
        } catch (Exception e) { if (!destroyed) lastError = message(e); }
    }

    private void stopGateway() throws Exception {
        Gateway old = gateway;
        if (activeGateway == old) activeGateway = null;
        lastStatus = "";
        lastStatusAt = 0;
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
        if (destroyed) return;
        phase = "failed";
        lastError = ConfigStore.redact(this, message(e));
        lastState = "Gateway-Fehler";
        updateNotification(lastState);
    }
    private static String message(Exception e) { return e.getMessage() == null ? e.toString() : e.getMessage(); }

    // Continuous remote-doorbell monitoring is the user-requested operation.
    // Releasing after a timer would silently break screen-off calls. Locks are
    // released on stop, failed startup, runtime exit and service destruction.
    @android.annotation.SuppressLint("WakelockTimeout")
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
                            try { phase = "starting"; lastState = "Netzwerk geändert – Neustart"; stopGateway(); startGateway(); }
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
        if (currentService == this) phase = "stopping";
        if (polling != null) polling.cancel(false);
        if (networkRestart != null) networkRestart.cancel(false);
        if (connectivity != null && networkCallback != null) connectivity.unregisterNetworkCallback(networkCallback);
        worker.execute(() -> {
            try { stopGateway(); }
            catch (Exception e) { lastError = message(e); }
            finally {
                releaseLocks();
                if (currentService == this) {
                    currentService = null;
                    phase = "stopped";
                    lastState = "Gestoppt";
                    lastStatus = "";
                    lastStatusAt = 0;
                }
            }
        });
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
