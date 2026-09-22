package de.vothmarkus.reolinksip;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;

import java.io.File;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;

import de.vothmarkus.reolinksip.core.mobilebridge.Gateway;
import de.vothmarkus.reolinksip.core.mobilebridge.Listener;
import de.vothmarkus.reolinksip.core.mobilebridge.Mobilebridge;

public final class GatewayService extends Service {
    static final String ACTION_STOP = "de.vothmarkus.reolinksip.STOP";
    private static final String CHANNEL = "gateway";
    private static final int NOTIFICATION_ID = 4102;

    private static volatile String lastState = "gestoppt";
    private static volatile String lastStatus = "";
    private static volatile String lastError = "";

    private Gateway gateway;
    private MediaCodecAudioAdapter audioAdapter;
    private ScheduledExecutorService poller;

    static void start(Context context) {
        Intent intent = new Intent(context, GatewayService.class);
        context.startForegroundService(intent);
    }

    static void stop(Context context) {
        Intent intent = new Intent(context, GatewayService.class);
        intent.setAction(ACTION_STOP);
        context.startForegroundService(intent);
    }

    static String summary() {
        if (!lastError.isEmpty()) {
            return lastState + "\nFehler: " + lastError + (lastStatus.isEmpty() ? "" : "\n\n" + lastStatus);
        }
        return lastState + (lastStatus.isEmpty() ? "" : "\n\n" + lastStatus);
    }

    static void testCall() throws Exception {
        Gateway g = Holder.gateway;
        if (g == null) throw new IllegalStateException("Gateway läuft nicht");
        g.testCall("default");
    }

    static void hangup() throws Exception {
        Gateway g = Holder.gateway;
        if (g == null) throw new IllegalStateException("Gateway läuft nicht");
        g.hangup();
    }

    private static final class Holder {
        static volatile Gateway gateway;
    }

    @Override
    public void onCreate() {
        super.onCreate();
        createNotificationChannel();
        startAsForeground("Gateway wird gestartet");
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        if (intent != null && ACTION_STOP.equals(intent.getAction())) {
            stopGateway();
            stopSelf();
            return START_NOT_STICKY;
        }
        if (gateway == null) {
            try {
                String json = ConfigStore.buildJson(this);
                File dataDir = new File(getFilesDir(), "gateway");
                audioAdapter = new MediaCodecAudioAdapter();
                gateway = Mobilebridge.start(json, dataDir.getAbsolutePath(), audioAdapter, new Listener() {
                    @Override
                    public void onState(String state) {
                        lastState = state;
                        updateNotification("Status: " + state);
                    }

                    @Override
                    public void onError(String message) {
                        lastError = message;
                        updateNotification("Fehler im Gateway");
                    }
                });
                Holder.gateway = gateway;
                lastError = "";
                lastState = "gestartet";
                startPolling();
            } catch (Exception e) {
                lastError = e.getMessage() == null ? e.toString() : e.getMessage();
                lastState = "Start fehlgeschlagen";
                updateNotification("Start fehlgeschlagen");
            }
        }
        return START_STICKY;
    }

    private void startPolling() {
        poller = Executors.newSingleThreadScheduledExecutor();
        poller.scheduleWithFixedDelay(() -> {
            Gateway g = gateway;
            if (g == null) return;
            try {
                lastStatus = g.statusJSON();
                lastError = g.lastError();
            } catch (Exception e) {
                if (g.isRunning()) {
                    lastError = e.getMessage() == null ? e.toString() : e.getMessage();
                }
            }
        }, 1, 2, TimeUnit.SECONDS);
    }

    private void stopGateway() {
        if (poller != null) {
            poller.shutdownNow();
            poller = null;
        }
        Gateway g = gateway;
        gateway = null;
        Holder.gateway = null;
        if (g != null) {
            try {
                g.stop();
            } catch (Exception e) {
                lastError = e.getMessage() == null ? e.toString() : e.getMessage();
            }
        }
        if (audioAdapter != null) {
            audioAdapter.closeAll();
            audioAdapter = null;
        }
        lastState = "gestoppt";
        lastStatus = "";
    }

    @Override
    public void onDestroy() {
        stopGateway();
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }

    private void createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= 26) {
            NotificationChannel channel = new NotificationChannel(
                    CHANNEL, "Reolink SIP Gateway", NotificationManager.IMPORTANCE_LOW);
            getSystemService(NotificationManager.class).createNotificationChannel(channel);
        }
    }

    private Notification notification(String text) {
        Intent open = new Intent(this, MainActivity.class);
        PendingIntent pending = PendingIntent.getActivity(
                this, 0, open, PendingIntent.FLAG_IMMUTABLE | PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this, CHANNEL)
                .setSmallIcon(android.R.drawable.stat_sys_phone_call)
                .setContentTitle("Reolink SIP Gateway")
                .setContentText(text)
                .setContentIntent(pending)
                .setOngoing(true)
                .build();
    }

    private void startAsForeground(String text) {
        Notification n = notification(text);
        if (Build.VERSION.SDK_INT >= 29) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE);
        } else {
            startForeground(NOTIFICATION_ID, n);
        }
    }

    private void updateNotification(String text) {
        getSystemService(NotificationManager.class).notify(NOTIFICATION_ID, notification(text));
    }
}
