package de.vothmarkus.reolinksip;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.view.View;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends Activity {
    private EditText host;
    private EditText user;
    private EditText password;
    private EditText channel;
    private EditText registrar;
    private EditText sipUser;
    private EditText sipPassword;
    private EditText doorNumber;
    private CheckBox dryRun;
    private CheckBox startOnBoot;
    private TextView status;
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final ExecutorService executor = Executors.newSingleThreadExecutor();

    private final Runnable refresh = new Runnable() {
        @Override public void run() {
            status.setText(GatewayService.summary());
            handler.postDelayed(this, 2000);
        }
    };

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
        }

        ScrollView scroll = new ScrollView(this);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        int p = dp(16);
        root.setPadding(p, p, p, p);
        scroll.addView(root);

        TextView title = new TextView(this);
        title.setText("Reolink SIP Gateway – Android Alpha");
        title.setTextSize(22);
        root.addView(title);

        addSection(root, "Reolink / NVR");
        host = addField(root, "NVR-IP / Host", ConfigStore.get(this, "reolink_host", ""));
        user = addField(root, "Reolink Benutzer", ConfigStore.get(this, "reolink_user", "admin"));
        password = addPassword(root, "Reolink Passwort", ConfigStore.get(this, "reolink_password", ""));
        channel = addField(root, "NVR-Kanal (1-basiert)", String.valueOf(ConfigStore.prefs(this).getInt("nvr_channel", 1)));

        addSection(root, "FRITZ!Box / SIP");
        registrar = addField(root, "FRITZ!Box / SIP Registrar", ConfigStore.get(this, "sip_registrar", ""));
        sipUser = addField(root, "SIP Benutzer", ConfigStore.get(this, "sip_user", ""));
        sipPassword = addPassword(root, "SIP Passwort", ConfigStore.get(this, "sip_password", ""));
        doorNumber = addField(root, "Klingeltaste / Ziel", ConfigStore.get(this, "door_number", "11"));

        addSection(root, "Betrieb");
        dryRun = new CheckBox(this);
        dryRun.setText("Passivmodus (zunächst empfohlen)");
        dryRun.setChecked(ConfigStore.getBool(this, "dry_run", true));
        root.addView(dryRun);
        startOnBoot = new CheckBox(this);
        startOnBoot.setText("Nach Neustart automatisch starten");
        startOnBoot.setChecked(ConfigStore.getBool(this, "start_on_boot", false));
        root.addView(startOnBoot);

        TextView aec = new TextView(this);
        aec.setText("AEC: in dieser ersten Android-Alpha noch deaktiviert. AAC wird nativ über MediaCodec decodiert.");
        aec.setPadding(0, dp(8), 0, dp(12));
        root.addView(aec);

        addButton(root, "Speichern & Gateway starten", v -> {
            try {
                save();
                GatewayService.start(this);
                toast("Gateway-Start angefordert");
            } catch (Exception e) {
                toast(e.getMessage());
            }
        });
        addButton(root, "Gateway stoppen", v -> GatewayService.stop(this));
        addButton(root, "Testanruf", v -> runAsync(() -> GatewayService.testCall()));
        addButton(root, "Auflegen", v -> runAsync(() -> GatewayService.hangup()));
        addButton(root, "Akkuoptimierung öffnen", v ->
                startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)));

        addSection(root, "Status");
        status = new TextView(this);
        status.setTextIsSelectable(true);
        status.setText(GatewayService.summary());
        root.addView(status);

        setContentView(scroll);
    }

    @Override protected void onResume() {
        super.onResume();
        handler.post(refresh);
    }

    @Override protected void onPause() {
        handler.removeCallbacks(refresh);
        super.onPause();
    }

    @Override protected void onDestroy() {
        executor.shutdownNow();
        super.onDestroy();
    }

    private void save() throws Exception {
        int n = Integer.parseInt(channel.getText().toString().trim());
        if (n < 1 || n > 256) throw new IllegalArgumentException("NVR-Kanal muss 1..256 sein");
        if (host.getText().toString().trim().isEmpty()) throw new IllegalArgumentException("Reolink/NVR Host fehlt");
        if (registrar.getText().toString().trim().isEmpty()) throw new IllegalArgumentException("SIP Registrar fehlt");
        ConfigStore.save(this,
                host.getText().toString().trim(),
                user.getText().toString().trim(),
                password.getText().toString(),
                n,
                registrar.getText().toString().trim(),
                sipUser.getText().toString().trim(),
                sipPassword.getText().toString(),
                doorNumber.getText().toString().trim(),
                dryRun.isChecked(),
                startOnBoot.isChecked());
    }

    private void runAsync(ThrowingAction action) {
        executor.execute(() -> {
            try {
                action.run();
                runOnUiThread(() -> toast("OK"));
            } catch (Exception e) {
                runOnUiThread(() -> toast(e.getMessage() == null ? e.toString() : e.getMessage()));
            }
        });
    }

    private EditText addField(LinearLayout root, String label, String value) {
        TextView l = new TextView(this);
        l.setText(label);
        root.addView(l);
        EditText e = new EditText(this);
        e.setSingleLine(true);
        e.setText(value);
        root.addView(e);
        return e;
    }

    private EditText addPassword(LinearLayout root, String label, String value) {
        EditText e = addField(root, label, value);
        e.setInputType(0x00000081);
        return e;
    }

    private void addSection(LinearLayout root, String text) {
        TextView v = new TextView(this);
        v.setText(text);
        v.setTextSize(18);
        v.setPadding(0, dp(20), 0, dp(6));
        root.addView(v);
    }

    private void addButton(LinearLayout root, String text, View.OnClickListener listener) {
        Button b = new Button(this);
        b.setText(text);
        b.setOnClickListener(listener);
        root.addView(b);
    }

    private void toast(String message) {
        Toast.makeText(this, message == null ? "" : message, Toast.LENGTH_LONG).show();
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private interface ThrowingAction { void run() throws Exception; }
}
