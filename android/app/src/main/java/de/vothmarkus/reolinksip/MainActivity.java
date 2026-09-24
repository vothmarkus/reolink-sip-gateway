package de.vothmarkus.reolinksip;

import android.Manifest;
import android.app.Activity;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.text.format.DateFormat;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.provider.Settings;
import android.text.InputType;
import android.view.View;
import android.widget.*;
import org.json.JSONObject;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import de.vothmarkus.reolinksip.core.mobilebridge.Mobilebridge;

public final class MainActivity extends Activity {
    private final Map<String, EditText> fields = new LinkedHashMap<>();
    private final Map<String, CheckBox> checks = new LinkedHashMap<>();
    private final Map<String, int[]> ranges = new LinkedHashMap<>();
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final ExecutorService executor = Executors.newSingleThreadExecutor();
    private final ExecutorService imageExecutor = Executors.newSingleThreadExecutor();
    private ImageView image;
    private TextView imageStatus;
    private boolean resumed, previewRequested, imageBusy;
    private final Runnable imageRefresh = this::loadImage;
    private JSONObject values;
    private Spinner mode, codec;
    private TextView status;
    private Button testCall, hangup, save;
    private LinearLayout channelGroup;
    private final Runnable refresh = new Runnable() {
        @Override public void run() {
            status.setText(ConfigStore.redact(MainActivity.this, GatewayService.summary()));
            testCall.setEnabled(GatewayService.testAvailable());
            hangup.setEnabled(GatewayService.hangupAvailable());
            handler.postDelayed(this, 2000);
        }
    };

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        values = ConfigStore.read(this);
        if (state != null && state.containsKey("form")) {
            try { values = new JSONObject(state.getString("form")); } catch (Exception ignored) {}
        }
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED)
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
        ScrollView scroll = new ScrollView(this);
        LinearLayout root = group();
        root.setPadding(dp(16), dp(12), dp(16), dp(24));
        root.setOnApplyWindowInsetsListener((v, insets) -> {
            v.setPadding(dp(16) + insets.getSystemWindowInsetLeft(), dp(12) + insets.getSystemWindowInsetTop(),
                    dp(16) + insets.getSystemWindowInsetRight(), dp(24) + insets.getSystemWindowInsetBottom());
            return insets;
        });
        scroll.addView(root);
        section(root, "Reolink SIP Gateway · 0.3.2 Alpha 6");
        note(root, "Kamera und Telefonanlage direkt verbinden – ohne Home Assistant. Für den Betrieb ohne NVR die eigene IP der Kamera verwenden.");
        section(root, "Status");
        status = new TextView(this);
        status.setTextIsSelectable(true);
        root.addView(status);
        testCall = button(root, "Testanruf", v -> runAsync(GatewayService::testCall));
        hangup = button(root, "Auflegen", v -> runAsync(GatewayService::hangup));
        button(root, "Diagnose kopieren", v -> {
            ClipboardManager clipboard = getSystemService(ClipboardManager.class);
            clipboard.setPrimaryClip(ClipData.newPlainText("Gateway-Diagnose", ConfigStore.redact(this,
                    "Android-App 0.3.2-alpha6\n" + GatewayService.summary() + "\n\n" + GatewayService.rawStatus())));
            toast("Diagnose kopiert");
        });

        section(root, "Reolink");
        mode = spinner(root, new String[]{"Kamera direkt (ohne NVR)", "Über NVR"}, ConfigStore.deviceMode(values).equals("nvr") ? 1 : 0);
        field(root, "reolink_host", "Kamera- bzw. NVR-IP / Host", "", false);
        field(root, "reolink_user", "Reolink-Benutzer", "admin", false);
        field(root, "reolink_password", "Reolink-Passwort", "", true);
        number(root, "baichuan_port", "Basic-Service-Port", 9000, 1, 65535);
        channelGroup = group(); root.addView(channelGroup);
        number(channelGroup, "nvr_channel", "NVR-Kanal (1 bis 256)", 1, 1, 256);
        channelGroup.setVisibility(mode.getSelectedItemPosition() == 1 ? View.VISIBLE : View.GONE);
        mode.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            public void onItemSelected(AdapterView<?> p, View v, int position, long id) {
                channelGroup.setVisibility(position == 1 ? View.VISIBLE : View.GONE);
            }
            public void onNothingSelected(AdapterView<?> p) {}
        });
        note(root, "An der Kamera muss der Basic Service (normalerweise TCP 9000) aktiviert sein. Im direkten Modus wird der Kamerakanal automatisch gewählt.");

        section(root, "FRITZ!Box / SIP");
        field(root, "sip_registrar", "FRITZ!Box / SIP-Registrar", "", false);
        note(root, "Die Adresse der Telefonanlage eintragen, auch wenn ein anderer Router das Standardgateway ist.");
        check(root, "door_enabled", "Tür-Anrufkonto aktivieren", true);
        field(root, "sip_user", "SIP-Benutzer Tür", "", false);
        field(root, "sip_password", "SIP-Passwort Tür", "", true);
        field(root, "door_number", "Klingeltaste / Rufziel", "11", false);
        field(root, "display_name", "Anzeigename", "Haustür", false);

        LinearLayout advanced = expandable(root, "Weitere SIP-Einstellungen");
        number(advanced, "sip_port", "SIP-Registrar-Port", 5060, 1, 65535);
        number(advanced, "sip_local_port", "Lokaler SIP-Port Tür", 5070, 1, 65535);
        note(advanced, "Codec");
        String selectedCodec = values.optString("codec", "pcma");
        codec = spinner(advanced, new String[]{"PCMA (G.711 A-law)", "PCMU (G.711 µ-law)", "Automatisch"},
                selectedCodec.equals("pcmu") ? 1 : selectedCodec.equals("auto") ? 2 : 0);

        LinearLayout incoming = expandable(root, "Eingehende Anrufe");
        check(incoming, "incoming_enabled", "Kamera vom Telefon aus anrufen", false);
        field(incoming, "allowed_callers", "Erlaubte Rufnummern (Komma-getrennt; * = alle)", "*", false);
        check(incoming, "connection_tone", "Verbindungston an der Kamera", true);

        LinearLayout parallel = expandable(root, "Parallelruf");
        check(parallel, "parallel_enabled", "Weitere Ziele gleichzeitig anrufen", false);
        note(parallel, "Verwendet wie die HA-Version ein zweites SIP-Konto. Das erste angenommene Gespräch gewinnt; die übrigen Anrufe werden beendet.");
        field(parallel, "parallel_user", "SIP-Benutzer Parallelruf", "", false);
        field(parallel, "parallel_password", "SIP-Passwort Parallelruf", "", true);
        number(parallel, "parallel_port", "Lokaler SIP-Port Parallelruf", 5071, 1, 65535);
        for (int i = 1; i <= 3; i++) field(parallel, "mobile_" + i, "Zusätzliches Rufziel " + i, "", false);

        LinearLayout timing = expandable(root, "Zeitlimits");
        number(timing, "ring_timeout", "Klingeldauer (Sekunden)", 30, 5, 180);
        number(timing, "max_duration", "Maximale Gesprächsdauer (Sekunden)", 300, 15, 3600);
        number(timing, "rtp_timeout", "Abbruch ohne Telefonaudio (Sekunden)", 15, 5, 120);
        number(timing, "debounce", "Klingelsperre nach Auslösung (Sekunden)", 3, 0, 60);

        LinearLayout audio = expandable(root, "Audio / WebRTC");
        check(audio, "aec_enabled", "WebRTC-Echo-Unterdrückung aktivieren", false);
        check(audio, "high_pass", "Hochpassfilter bei aktivem AEC", true);
        check(audio, "noise_suppression", "Rauschunterdrückung bei aktivem AEC", true);
        note(audio, "Beim Start mit AEC wird die Kameraverzögerung mit einem hörbaren Testsignal gemessen. Die Vorbereitung kann etwa eine Minute dauern. Das Android-Mikrofon wird nicht benötigt.");

        section(root, "Live-Bild");
        check(root, "live_image", "Live-Bild für App und FRITZ!Fon aktivieren", false);
        number(root, "image_port", "Live-Bild-Port am Android-Gerät", 18099, 1024, 65535);
        note(root, "HTTP oder HTTPS an der Kamera aktivieren. Änderungen unten speichern. Die Vorschau aktualisiert JPEG-Bilder alle 3 Sekunden, solange die App geöffnet ist.");
        imageStatus = new TextView(this); root.addView(imageStatus);
        image = new ImageView(this); image.setAdjustViewBounds(true); image.setMaxHeight(dp(360));
        image.setContentDescription("Aktuelles Kamerabild"); image.setScaleType(ImageView.ScaleType.FIT_CENTER);
        root.addView(image, new LinearLayout.LayoutParams(-1, -2));
        button(root, "Vorschau starten / stoppen", v -> {
            previewRequested = !previewRequested;
            handler.removeCallbacks(imageRefresh);
            if (previewRequested) loadImage();
            else { image.setImageDrawable(null); imageStatus.setText("Vorschau gestoppt"); }
        });
        button(root, "FRITZ!Fon-Bildadresse kopieren", v -> imageExecutor.execute(() -> {
            try {
                String url = GatewayService.liveImageURL();
                runOnUiThread(() -> {
                    if (isDestroyed()) return;
                    getSystemService(ClipboardManager.class).setPrimaryClip(ClipData.newPlainText("FRITZ!Fon Live-Bild", url));
                    toast("Bildadresse kopiert; in der FRITZ!Box als Live-Bild-Adresse eintragen");
                });
            } catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
        }));
        note(root, "In der FRITZ!Box dem Android-Gerät eine feste IPv4-Adresse zuweisen. Die Bildadresse enthält einen Zugangsschlüssel und ist für das lokale Netz bestimmt.");

        section(root, "Betrieb");
        check(root, "dry_run", "Passivmodus: Klingeln erkennen, SIP und Anrufe aus", true);
        check(root, "start_on_boot", "Nach Geräteneustart automatisch starten", false);
        note(root, "Der Dienst hält das Gerät bei ausgeschaltetem Bildschirm betriebsbereit. Für den Dauerbetrieb am Strom lassen und die Akkuoptimierung der App deaktivieren.");
        button(root, "WebRTC-Lizenzen", v -> {
            try (java.io.InputStream input = getAssets().open("webrtc-notices.txt")) {
                java.io.ByteArrayOutputStream buffer = new java.io.ByteArrayOutputStream();
                byte[] block = new byte[4096];
                int count;
                while ((count = input.read(block)) != -1) buffer.write(block, 0, count);
                String notices = new String(buffer.toByteArray(), java.nio.charset.StandardCharsets.UTF_8);
                new android.app.AlertDialog.Builder(this).setTitle("WebRTC / Abseil").setMessage(notices).setPositiveButton("Schließen", null).show();
            } catch (Exception e) { toast("Lizenztext nicht verfügbar"); }
        });
        save = button(root, "Speichern & Gateway neu starten", v -> saveAndRestart());
        button(root, "Gateway stoppen", v -> GatewayService.stop(this));
        button(root, "Akkuoptimierung öffnen", v -> {
            try { startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)); }
            catch (Exception e) { toast("Bitte Akkuoptimierung in den Android-App-Einstellungen öffnen"); }
        });
        setContentView(scroll);
    }

    private JSONObject collect(boolean validate) throws Exception {
        JSONObject p = new JSONObject(values.toString());
        for (Map.Entry<String, EditText> f : fields.entrySet()) {
            String text = f.getValue().getText().toString();
            int[] range = ranges.get(f.getKey());
            if (range != null) {
                if (f.getKey().equals("nvr_channel") && mode.getSelectedItemPosition() == 0) continue;
                try {
                    int value = Integer.parseInt(text.trim());
                    if (validate && (value < range[0] || value > range[1])) throw new NumberFormatException();
                    p.put(f.getKey(), value);
                } catch (NumberFormatException e) {
                    if (validate) throw new IllegalArgumentException(f.getValue().getContentDescription() + ": " + range[0] + " bis " + range[1]);
                    p.put(f.getKey(), text);
                }
            } else p.put(f.getKey(), text);
        }
        for (Map.Entry<String, CheckBox> c : checks.entrySet()) p.put(c.getKey(), c.getValue().isChecked());
        p.put("device_mode", mode.getSelectedItemPosition() == 0 ? "direct" : "nvr");
        p.put("codec", new String[]{"pcma", "pcmu", "auto"}[codec.getSelectedItemPosition()]);
        return p;
    }

    private void saveAndRestart() {
        try {
            JSONObject candidate = collect(true); // Read views only on the main thread.
            String config = ConfigStore.buildJson(candidate);
            save.setEnabled(false);
            executor.execute(() -> {
                try {
                    Mobilebridge.validateConfig(config);
                    ConfigStore.save(this, candidate); // Persist only a validated configuration.
                    runOnUiThread(() -> {
                        if (!isDestroyed()) {
                            try { GatewayService.restart(this); toast("Einstellungen gespeichert; Gateway startet neu"); }
                            catch (Exception e) { toast(e.getMessage()); }
                        }
                    });
                } catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
                finally { runOnUiThread(() -> save.setEnabled(true)); }
            });
        } catch (Exception e) { toast(e.getMessage()); }
    }

    private void loadImage() {
        if (!resumed || !previewRequested || imageBusy) return;
        imageBusy = true;
        imageStatus.setText("Bild wird geladen …");
        imageExecutor.execute(() -> {
            Bitmap bitmap = null;
            String failure = "";
            try {
                byte[] jpeg = GatewayService.snapshotJPEG();
                bitmap = BitmapFactory.decodeByteArray(jpeg, 0, jpeg.length);
                if (bitmap == null) throw new IllegalStateException("Kamerabild konnte nicht geöffnet werden");
            } catch (Exception e) { failure = e.getMessage(); }
            final Bitmap result = bitmap;
            final String error = failure;
            runOnUiThread(() -> {
                imageBusy = false;
                if (isDestroyed() || !resumed || !previewRequested) return;
                image.setImageBitmap(result);
                imageStatus.setText(result == null ? ConfigStore.redact(this, error == null ? "Bildabruf fehlgeschlagen" : error) :
                        "Kamerabild · " + DateFormat.format("HH:mm:ss", System.currentTimeMillis()));
                handler.removeCallbacks(imageRefresh);
                handler.postDelayed(imageRefresh, result == null ? 8000 : 3000);
            });
        });
    }

    @Override protected void onResume() { super.onResume(); resumed = true; handler.post(refresh); handler.post(imageRefresh); }
    @Override protected void onPause() { resumed = false; handler.removeCallbacks(refresh); handler.removeCallbacks(imageRefresh); super.onPause(); }
    @Override protected void onDestroy() { executor.shutdown(); imageExecutor.shutdown(); super.onDestroy(); }
    @Override protected void onSaveInstanceState(Bundle out) {
        try { out.putString("form", collect(false).toString()); } catch (Exception ignored) {}
        super.onSaveInstanceState(out);
    }
    private void runAsync(Action action) {
        executor.execute(() -> {
            try { action.run(); runOnUiThread(() -> toast("Anforderung ausgeführt")); }
            catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
        });
    }
    private LinearLayout group() { LinearLayout l = new LinearLayout(this); l.setOrientation(LinearLayout.VERTICAL); return l; }
    private LinearLayout expandable(LinearLayout root, String title) {
        LinearLayout child = group(); child.setVisibility(View.GONE);
        button(root, title + " ▾", v -> child.setVisibility(child.getVisibility() == View.GONE ? View.VISIBLE : View.GONE));
        root.addView(child); return child;
    }
    private void section(LinearLayout root, String label) {
        TextView v = new TextView(this); v.setText(label); v.setTextSize(20); v.setPadding(0, dp(20), 0, dp(8)); root.addView(v);
    }
    private void note(LinearLayout root, String label) {
        TextView v = new TextView(this); v.setText(label); v.setPadding(0, dp(6), 0, dp(8)); root.addView(v);
    }
    private EditText field(LinearLayout root, String key, String label, String def, boolean secret) {
        note(root, label);
        EditText e = new EditText(this); e.setSingleLine(true); e.setContentDescription(label);
        e.setInputType(InputType.TYPE_CLASS_TEXT | (secret ? InputType.TYPE_TEXT_VARIATION_PASSWORD : InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS));
        e.setText(values.optString(key, def)); root.addView(e); fields.put(key, e); return e;
    }
    private void number(LinearLayout root, String key, String label, int def, int min, int max) {
        field(root, key, label, Integer.toString(def), false).setInputType(InputType.TYPE_CLASS_NUMBER);
        ranges.put(key, new int[]{min, max});
    }
    private void check(LinearLayout root, String key, String label, boolean def) {
        CheckBox c = new CheckBox(this); c.setText(label); c.setChecked(values.optBoolean(key, def)); root.addView(c); checks.put(key, c);
    }
    private Spinner spinner(LinearLayout root, String[] choices, int selected) {
        Spinner s = new Spinner(this);
        ArrayAdapter<String> adapter = new ArrayAdapter<>(this, android.R.layout.simple_spinner_item, choices);
        adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item); s.setAdapter(adapter); s.setSelection(selected); root.addView(s); return s;
    }
    private Button button(LinearLayout root, String label, View.OnClickListener listener) {
        Button b = new Button(this); b.setText(label); b.setOnClickListener(listener); root.addView(b); return b;
    }
    private void toast(String message) { if (!isDestroyed()) Toast.makeText(this, message == null ? "Unbekannter Fehler" : ConfigStore.redact(this, message), Toast.LENGTH_LONG).show(); }
    private int dp(int n) { return Math.round(n * getResources().getDisplayMetrics().density); }
    private interface Action { void run() throws Exception; }
}
