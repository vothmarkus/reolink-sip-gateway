package de.vothmarkus.reolinksip;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.PersistableBundle;
import android.provider.Settings;
import android.text.InputType;
import android.text.format.DateFormat;
import android.view.Gravity;
import android.view.View;
import android.widget.*;
import org.json.JSONArray;
import org.json.JSONObject;
import java.io.ByteArrayOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import de.vothmarkus.reolinksip.core.mobilebridge.Mobilebridge;

public final class MainActivity extends Activity {
    private static final int GREEN = 0xff167663, INK = 0xff172e29, MUTED = 0xff526760;
    private static final int EXPORT = 701, IMPORT = 702, MAX_IMPORT_BYTES = 262144;
    private final Map<String, EditText> fields = new LinkedHashMap<>();
    private final Map<String, CheckBox> checks = new LinkedHashMap<>();
    private final Map<String, int[]> ranges = new LinkedHashMap<>();
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final ExecutorService executor = Executors.newSingleThreadExecutor();
    private final ExecutorService imageExecutor = Executors.newSingleThreadExecutor();
    private final List<String> testRouteIDs = new ArrayList<>(), triggerRouteIDs = new ArrayList<>();
    private final LinearLayout[] pages = new LinearLayout[3];
    private final Button[] tabs = new Button[3];
    private JSONObject values;
    private JSONArray routes;
    private String triggerRouteID, runningRoutes = "", pendingExport;
    private Spinner mode, codec, trigger, triggerRoute, logLevel, testRoute;
    private TextView status, switchStatus, overviewStatus, audioStatus, imageStatus;
    private Switch gatewaySwitch;
    private Button testCall, hangup, save, previewButton;
    private ImageView image;
    private LinearLayout routeList, channelGroup;
    private ScrollView scroll;
    private int selectedPage;
    private boolean updatingSwitch, saving, actionBusy, resumed, previewRequested, imageBusy;
    private final Runnable imageRefresh = this::loadImage;
    private final Runnable refresh = new Runnable() {
        @Override public void run() { refreshStatus(); handler.postDelayed(this, 1500); }
    };

    @Override public void onCreate(Bundle state) {
        super.onCreate(state);
        values = ConfigStore.read(this);
        if (state != null) {
            try { values = new JSONObject(state.getString("form", values.toString())); } catch (Exception ignored) {}
            selectedPage = state.getInt("page", 0); pendingExport = state.getString("export");
        }
        if (Build.VERSION.SDK_INT >= 33 && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED)
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, 1);
        render();
    }

    private void render() {
        fields.clear(); checks.clear(); ranges.clear(); runningRoutes = "";
        previewRequested = false; handler.removeCallbacks(imageRefresh);
        try { routes = ConfigStore.routes(values); }
        catch (Exception e) { toast("Gespeicherte Rufrouten sind ungültig"); routes = new JSONArray(); }
        triggerRouteID = values.optString("trigger_route", "default");
        LinearLayout root = group(); root.setPadding(dp(16), dp(12), dp(16), dp(12));
        root.setOnApplyWindowInsetsListener((v, insets) -> {
            v.setPadding(dp(16) + insets.getSystemWindowInsetLeft(), dp(12) + insets.getSystemWindowInsetTop(),
                    dp(16) + insets.getSystemWindowInsetRight(), dp(12) + insets.getSystemWindowInsetBottom());
            return insets;
        });
        TextView brand = label(root, "REOLINK  /  ANDROID " + BuildConfig.VERSION_NAME, 12, MUTED); brand.setLetterSpacing(0.10f);
        label(root, "SIP Gateway", 28, INK).setTypeface(null, Typeface.BOLD);
        LinearLayout navigation = new LinearLayout(this); root.addView(navigation);
        String[] names = {"Übersicht", "Einstellungen", "Diagnose"};
        for (int i = 0; i < names.length; i++) {
            final int page = i;
            Button tab = new Button(this); tab.setText(names[i]); tab.setTextSize(13); tab.setAllCaps(false);
            tab.setMinWidth(0); tab.setMinimumWidth(0); tab.setPadding(dp(4), dp(6), dp(4), dp(6));
            tab.setOnClickListener(v -> selectPage(page));
            navigation.addView(tab, new LinearLayout.LayoutParams(0, -2, 1)); tabs[i] = tab;
        }
        scroll = new ScrollView(this); scroll.setFillViewport(true);
        LinearLayout content = group(); scroll.addView(content);
        for (int i = 0; i < pages.length; i++) { pages[i] = group(); content.addView(pages[i]); }
        root.addView(scroll, new LinearLayout.LayoutParams(-1, 0, 1));
        buildOverview(pages[0]); buildSettings(pages[1]); buildDiagnostics(pages[2]);
        setContentView(root); selectPage(Math.max(0, Math.min(2, selectedPage))); refreshStatus();
    }

    private void buildOverview(LinearLayout root) {
        LinearLayout power = card(root);
        gatewaySwitch = new Switch(this); gatewaySwitch.setText("Gateway"); gatewaySwitch.setTextSize(22);
        gatewaySwitch.setTypeface(null, Typeface.BOLD); gatewaySwitch.setTextColor(INK);
        gatewaySwitch.setMinHeight(dp(56)); gatewaySwitch.setContentDescription("Gateway ein- oder ausschalten");
        power.addView(gatewaySwitch, new LinearLayout.LayoutParams(-1, -2)); switchStatus = label(power, "", 15, MUTED);
        gatewaySwitch.setOnCheckedChangeListener((button, enabled) -> {
            if (updatingSwitch) return;
            updateSwitch();
            if (enabled) requestStart();
            else confirmCallEnd("Gateway ausschalten", () -> { GatewayService.stop(this); refreshStatus(); });
        });
        LinearLayout connection = card(root); section(connection, "Verbindungen"); overviewStatus = label(connection, "", 16, INK);
        LinearLayout calls = card(root); section(calls, "Anrufen"); note(calls, "Rufroute für den Testanruf");
        testRoute = spinner(calls, new String[]{"Haustür"}, 0); testRoute.setOnItemSelectedListener(selected(this::refreshControls));
        LinearLayout actions = new LinearLayout(this); calls.addView(actions);
        testCall = rowButton(actions, "Testanruf", v -> {
            int index = testRoute.getSelectedItemPosition();
            if (index >= 0 && index < testRouteIDs.size()) { String id = testRouteIDs.get(index); runAsync(() -> GatewayService.testCall(id)); }
        });
        hangup = rowButton(actions, "Auflegen", v -> runAsync(GatewayService::hangup));
        LinearLayout audio = card(root); section(audio, "Audio & Laufzeitmessung"); audioStatus = label(audio, "", 15, INK);
        button(audio, "Messwerte in der Diagnose", v -> selectPage(2));
        LinearLayout picture = card(root); section(picture, "Live-Bild");
        imageStatus = label(picture, "Vorschau bei Bedarf starten. Live-Bild zuerst in den Einstellungen aktivieren.", 14, MUTED);
        image = new ImageView(this); image.setAdjustViewBounds(true); image.setMaxHeight(dp(360));
        image.setContentDescription("Aktuelles Kamerabild"); image.setScaleType(ImageView.ScaleType.FIT_CENTER);
        picture.addView(image, new LinearLayout.LayoutParams(-1, -2));
        previewButton = button(picture, "Vorschau starten", v -> {
            previewRequested = !previewRequested; handler.removeCallbacks(imageRefresh);
            previewButton.setText(previewRequested ? "Vorschau stoppen" : "Vorschau starten");
            if (previewRequested) loadImage(); else { image.setImageDrawable(null); imageStatus.setText("Vorschau gestoppt"); }
        });
        button(picture, "FRITZ!Fon-Bildadresse kopieren", v -> imageExecutor.execute(() -> {
            try {
                String url = GatewayService.liveImageURL();
                runOnUiThread(() -> { if (!isDestroyed()) { clipboard("FRITZ!Fon Live-Bild", url, true); toast("Bildadresse kopiert"); } });
            } catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
        }));
        note(root, "Direkt mit Kamera und Telefonanlage verbunden. Home Assistant oder NVR sind im direkten Kameramodus nicht erforderlich.");
    }

    private void buildSettings(LinearLayout root) {
        note(root, "Änderungen mit Speichern übernehmen. Ein laufendes Gateway startet dabei neu; ein ausgeschaltetes bleibt aus.");
        save = button(root, "Einstellungen speichern", v -> saveSettings(false));
        LinearLayout camera = expandable(root, "Kamera", "Direkte Verbindung oder NVR, Zugangsdaten", values.optString("reolink_host").isEmpty());
        mode = spinner(camera, new String[]{"Kamera direkt (ohne NVR)", "Über NVR"}, ConfigStore.deviceMode(values).equals("nvr") ? 1 : 0);
        field(camera, "reolink_host", "Kamera- bzw. NVR-IP / Host", "", false);
        field(camera, "reolink_user", "Reolink-Benutzer", "admin", false);
        field(camera, "reolink_password", "Reolink-Passwort", "", true);
        number(camera, "baichuan_port", "Basic-Service-Port", 9000, 1, 65535);
        channelGroup = group(); camera.addView(channelGroup); number(channelGroup, "nvr_channel", "NVR-Kanal (1 bis 256)", 1, 1, 256);
        channelGroup.setVisibility(mode.getSelectedItemPosition() == 1 ? View.VISIBLE : View.GONE);
        mode.setOnItemSelectedListener(selected(() -> channelGroup.setVisibility(mode.getSelectedItemPosition() == 1 ? View.VISIBLE : View.GONE)));
        note(camera, "Basic Service an der Kamera aktivieren (normalerweise TCP 9000). Im direkten Modus die eigene Kamera-IP verwenden.");
        LinearLayout sip = expandable(root, "Telefonanlage & SIP", "Türkonto, Parallelruf und eingehende Anrufe", false);
        field(sip, "sip_registrar", "FRITZ!Box / SIP-Registrar", "", false);
        number(sip, "sip_port", "SIP-Registrar-Port", 5060, 1, 65535);
        field(sip, "display_name", "Anzeigename", "Haustür", false);
        check(sip, "door_enabled", "Tür-Anrufkonto aktivieren", true);
        field(sip, "sip_user", "SIP-Benutzer Tür", "", false); field(sip, "sip_password", "SIP-Passwort Tür", "", true);
        number(sip, "sip_local_port", "Lokaler SIP-Port Tür", 5070, 1, 65535);
        note(sip, "Sprachcodec"); codec = spinner(sip, new String[]{"PCMA (G.711 A-law)", "PCMU (G.711 µ-law)", "Automatisch"},
                index(new String[]{"pcma", "pcmu", "auto"}, values.optString("codec", "pcma")));
        section(sip, "Parallelruf"); check(sip, "parallel_enabled", "Parallelruf aktivieren", false);
        note(sip, "Zweites SIP-Konto für bis zu drei zusätzliche Ziele je Rufroute. Das erste angenommene Gespräch gewinnt.");
        field(sip, "parallel_user", "SIP-Benutzer Parallelruf", "", false); field(sip, "parallel_password", "SIP-Passwort Parallelruf", "", true);
        number(sip, "parallel_port", "Lokaler SIP-Port Parallelruf", 5071, 1, 65535);
        section(sip, "Eingehende Anrufe"); check(sip, "incoming_enabled", "Kamera vom Telefon aus anrufen", false);
        field(sip, "allowed_callers", "Erlaubte Rufnummern (Komma-getrennt; * = alle)", "*", false);
        check(sip, "connection_tone", "Verbindungston an der Kamera", true);
        LinearLayout routing = expandable(root, "Klingeln & Rufrouten", "Rufziele bearbeiten und Klingelroute auswählen", false);
        note(routing, "Auslöser"); trigger = spinner(routing, new String[]{"Klingeltaste an der Kamera", "Nur manuell / Testanrufe"}, values.optString("trigger_source", "baichuan").equals("manual") ? 1 : 0);
        note(routing, "Rufroute beim Klingeln"); triggerRoute = spinner(routing, new String[]{}, 0);
        trigger.setOnItemSelectedListener(selected(() -> triggerRoute.setEnabled(trigger.getSelectedItemPosition() == 0)));
        routeList = group(); routing.addView(routeList); button(routing, "Rufroute hinzufügen", v -> editRoute(-1)); rebuildRoutes();
        note(routing, "Alle Routen verwenden dieselbe Kamera und dieselben SIP-Konten. Eine Route wird durch die Klingeltaste ausgelöst; jede Route lässt sich einzeln testen. Es ist ein Gespräch gleichzeitig möglich.");
        LinearLayout timing = expandable(root, "Anrufzeiten", "Klingeldauer, Gesprächslimit und Klingelsperre", false);
        number(timing, "ring_timeout", "Klingeldauer (Sekunden)", 30, 5, 180); number(timing, "max_duration", "Maximale Gesprächsdauer (Sekunden)", 300, 15, 3600);
        number(timing, "rtp_timeout", "Abbruch ohne Telefonaudio (Sekunden)", 15, 5, 120); number(timing, "debounce", "Klingelsperre nach Auslösung (Sekunden)", 3, 0, 60);
        LinearLayout audio = expandable(root, "Audio & WebRTC", "Echo-Unterdrückung, Filter und Messung", false);
        check(audio, "aec_enabled", "WebRTC-Echo-Unterdrückung aktivieren", false); check(audio, "high_pass", "Hochpassfilter bei aktivem AEC", true);
        check(audio, "noise_suppression", "Rauschunterdrückung bei aktivem AEC", true);
        note(audio, "Bei aktivem AEC misst das Gateway beim Start mit einem hörbaren Testsignal die Kameraverzögerung. Das kann etwa eine Minute dauern. Das Android-Mikrofon wird nicht verwendet.");
        LinearLayout picture = expandable(root, "Live-Bild", "App-Vorschau und FRITZ!Fon-Bildadresse", false);
        check(picture, "live_image", "Live-Bild aktivieren", false); number(picture, "image_port", "Live-Bild-Port am Android-Gerät", 18099, 1024, 65535);
        note(picture, "HTTP oder HTTPS an der Kamera aktivieren. Die Übersicht lädt JPEG-Bilder alle 3 Sekunden. Dem Android-Gerät eine feste IPv4-Adresse zuweisen. Die Bildadresse enthält einen Zugangsschlüssel für das lokale Netz.");
        LinearLayout operation = expandable(root, "Betrieb", "Autostart, Passivmodus und Protokollierung", false);
        check(operation, "dry_run", "Passivmodus: Klingeln erkennen, SIP und Anrufe aus", true); check(operation, "start_on_boot", "Nach Geräteneustart automatisch starten", false);
        note(operation, "Ein am Hauptschalter ausgeschaltetes Gateway bleibt auch nach einem Neustart oder App-Update aus. Ein eingeschaltetes Gateway wird nach einem App-Update wieder gestartet.");
        note(operation, "Protokollierungsstufe"); logLevel = spinner(operation, new String[]{"Info (Standard)", "Debug", "Warnungen", "Fehler"},
                index(new String[]{"info", "debug", "warn", "error"}, values.optString("log_level", "info").replace("warning", "warn")));
        note(operation, "Für den Dauerbetrieb am Strom lassen und Akkuoptimierung deaktivieren. Protokolle bleiben Android-Systemprotokolle; die Diagnose zeigt den aktuellen Gateway-Status.");
        button(operation, "Akkuoptimierung öffnen", v -> {
            try { startActivity(new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS)); }
            catch (Exception e) { toast("Bitte Akkuoptimierung in den Android-App-Einstellungen öffnen"); }
        });
        LinearLayout backup = expandable(root, "Sicherung & Wiederherstellung", "Konfiguration als JSON exportieren oder laden", false);
        note(backup, "Das Dateiformat entspricht der Standalone-Version (Schema 2). Zugangsdaten sind enthalten. Autostart und Schaltzustand gehören zu diesem Gerät und werden nicht exportiert.");
        button(backup, "Gespeicherte Konfiguration exportieren", v -> exportConfig());
        button(backup, "Konfiguration aus Datei laden", v -> new AlertDialog.Builder(this).setTitle("Konfiguration laden")
                .setMessage("Die Datei ersetzt die ungespeicherten Eingaben. Nach der Prüfung kannst du sie mit Speichern übernehmen.")
                .setNegativeButton("Abbrechen", null).setPositiveButton("Datei auswählen", (d, w) -> {
                    try { startActivityForResult(new Intent(Intent.ACTION_OPEN_DOCUMENT).setType("application/json").addCategory(Intent.CATEGORY_OPENABLE), IMPORT); }
                    catch (Exception e) { toast("Dateiauswahl nicht verfügbar"); }
                }).show());
    }

    private void buildDiagnostics(LinearLayout root) {
        LinearLayout details = card(root); section(details, "Gateway-Diagnose");
        note(details, "Status, Kameraverbindung und Messwerte der letzten Audioverbindung. Passwörter werden beim Kopieren entfernt.");
        button(details, "Diagnose kopieren", v -> {
            clipboard("Gateway-Diagnose", ConfigStore.redact(this, "Android-App " + BuildConfig.VERSION_NAME + "\n" + GatewayService.summary() + "\n\n" + GatewayService.rawStatus()), false); toast("Diagnose kopiert");
        });
        status = label(details, "", 14, INK); status.setTextIsSelectable(true);
        button(root, "WebRTC-Lizenzen", v -> {
            try (InputStream input = getAssets().open("webrtc-notices.txt")) {
                new AlertDialog.Builder(this).setTitle("WebRTC / Abseil").setMessage(readText(input, 2 * 1024 * 1024)).setPositiveButton("Schließen", null).show();
            } catch (Exception e) { toast("Lizenztext nicht verfügbar"); }
        });
        note(root, "Android " + BuildConfig.VERSION_NAME + " · Gateway " + Mobilebridge.version() + "\nEigenständiger Betrieb ohne Home Assistant.");
    }

    private void selectPage(int page) {
        selectedPage = page;
        for (int i = 0; i < pages.length; i++) {
            pages[i].setVisibility(i == page ? View.VISIBLE : View.GONE); tabs[i].setTextColor(i == page ? GREEN : MUTED);
            tabs[i].setTypeface(null, i == page ? Typeface.BOLD : Typeface.NORMAL); tabs[i].setSelected(i == page);
        }
        scroll.scrollTo(0, 0); handler.removeCallbacks(imageRefresh); if (page == 0) loadImage();
    }

    private void updateSwitch() {
        updatingSwitch = true; gatewaySwitch.setChecked(GatewayService.enabled()); updatingSwitch = false;
        gatewaySwitch.setEnabled(!saving && !GatewayService.busy()); switchStatus.setText(GatewayService.phaseLabel());
        switchStatus.setTextColor(GatewayService.enabled() ? GREEN : MUTED);
    }

    private void refreshStatus() {
        updateSwitch(); status.setText(ConfigStore.redact(this, GatewayService.summary()));
        overviewStatus.setText(ConfigStore.redact(this, GatewayStatus.overview(GatewayService.rawStatus(), GatewayService.error())));
        audioStatus.setText(ConfigStore.redact(this, GatewayStatus.audioOverview(GatewayService.rawStatus())));
        String raw = GatewayService.rawStatus();
        try {
            JSONArray available = raw.isEmpty() ? new JSONArray() : new JSONObject(raw).optJSONArray("routes");
            if (available == null) available = new JSONArray();
            JSONArray labels = new JSONArray();
            for (int i = 0; i < available.length(); i++) {
                JSONObject r = available.getJSONObject(i); labels.put(new JSONObject().put("id", r.getString("id")).put("name", r.getString("name")));
            }
            if (!labels.toString().equals(runningRoutes)) {
                String previous = testRoute.getSelectedItemPosition() >= 0 && testRoute.getSelectedItemPosition() < testRouteIDs.size() ? testRouteIDs.get(testRoute.getSelectedItemPosition()) : triggerRouteID;
                runningRoutes = labels.toString(); testRouteIDs.clear(); List<String> names = new ArrayList<>();
                for (int i = 0; i < labels.length(); i++) { JSONObject r = labels.getJSONObject(i); testRouteIDs.add(r.getString("id")); names.add(r.getString("name")); }
                if (names.isEmpty()) names.add("Gateway starten, um Routen zu laden");
                setChoices(testRoute, names.toArray(new String[0]), Math.max(0, testRouteIDs.indexOf(previous)));
            }
        } catch (Exception ignored) { testRouteIDs.clear(); }
        refreshControls();
    }

    private void refreshControls() {
        if (testCall == null || hangup == null) return;
        int index = testRoute.getSelectedItemPosition();
        boolean available = index >= 0 && index < testRouteIDs.size() && GatewayStatus.routeAvailable(GatewayService.rawStatus(), testRouteIDs.get(index));
        testCall.setEnabled(!actionBusy && !saving && !GatewayService.busy() && GatewayService.testAvailable() && available);
        hangup.setEnabled(!actionBusy && GatewayService.hangupAvailable()); testRoute.setEnabled(!testRouteIDs.isEmpty() && !actionBusy);
        if (save != null) save.setEnabled(!saving && !GatewayService.busy());
    }

    private JSONObject collect(boolean validate) throws Exception {
        JSONObject p = new JSONObject(values.toString());
        for (Map.Entry<String, EditText> f : fields.entrySet()) {
            String text = f.getValue().getText().toString(); int[] range = ranges.get(f.getKey());
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
        p.put("routes_json", routes.toString()); p.put("trigger_route", triggerRouteID);
        p.put("trigger_source", trigger.getSelectedItemPosition() == 0 ? "baichuan" : "manual");
        p.put("log_level", new String[]{"info", "debug", "warn", "error"}[logLevel.getSelectedItemPosition()]); return p;
    }

    private void requestStart() {
        // Starting explicitly applies the visible form, avoiding a silent start
        // with different (previously saved) credentials or routing destinations.
        saveSettings(true);
    }

    private void saveSettings(boolean start) {
        if (saving) return;
        try {
            JSONObject candidate = collect(true); String config = ConfigStore.buildJson(candidate);
            confirmCallEnd("Einstellungen übernehmen", () -> persist(candidate, config, start));
        } catch (Exception e) { toast(e.getMessage()); selectPage(1); }
    }

    private void persist(JSONObject candidate, String config, boolean start) {
        saving = true; refreshStatus();
        executor.execute(() -> {
            try {
                Mobilebridge.validateConfig(config); ConfigStore.save(this, candidate);
                // Complete a user-requested action even if this Activity rotates.
                boolean restart = GatewayService.enabled();
                if (restart) GatewayService.restart(getApplicationContext()); else if (start) GatewayService.start(getApplicationContext());
                runOnUiThread(() -> {
                    if (isDestroyed()) return;
                    values = candidate; toast(restart ? "Gespeichert. Gateway startet neu." : start ? "Gateway startet." : "Gespeichert. Gateway bleibt ausgeschaltet.");
                });
            } catch (Exception e) { runOnUiThread(() -> { toast(e.getMessage()); if (!isDestroyed()) selectPage(1); }); }
            finally { runOnUiThread(() -> { saving = false; if (!isDestroyed()) refreshStatus(); }); }
        });
    }

    private void confirmCallEnd(String title, Action action) {
        Runnable execute = () -> { try { action.run(); } catch (Exception e) { toast(e.getMessage()); } };
        if (!GatewayService.hangupAvailable()) { execute.run(); return; }
        new AlertDialog.Builder(this).setTitle(title).setMessage("Das laufende Gespräch bzw. der Rufversuch wird dabei beendet.")
                .setNegativeButton("Abbrechen", null).setPositiveButton("Fortfahren", (d, w) -> execute.run()).show();
    }

    private void rebuildRoutes() {
        routeList.removeAllViews(); triggerRoute.setOnItemSelectedListener(null); triggerRouteIDs.clear(); List<String> names = new ArrayList<>();
        for (int i = 0; i < routes.length(); i++) {
            final int position = i; JSONObject route = routes.optJSONObject(i); if (route == null) continue;
            String id = route.optString("id"), name = route.optString("name", id); triggerRouteIDs.add(id); names.add(name);
            LinearLayout row = group(); routeList.addView(row); section(row, name); StringBuilder targets = new StringBuilder();
            if (!route.optString("doorbell_number").isEmpty()) targets.append("Tür: ").append(route.optString("doorbell_number"));
            for (int n = 1; n <= 3; n++) if (!route.optString("mobile_number_" + n).isEmpty()) {
                if (targets.length() > 0) targets.append(" · "); targets.append(route.optString("mobile_number_" + n));
            }
            note(row, targets.length() == 0 ? "Noch keine Rufziele" : targets.toString());
            LinearLayout actions = new LinearLayout(this); row.addView(actions); rowButton(actions, "Bearbeiten", v -> editRoute(position));
            Button remove = rowButton(actions, "Entfernen", v -> new AlertDialog.Builder(this).setTitle("Rufroute entfernen?").setMessage(name)
                    .setNegativeButton("Abbrechen", null).setPositiveButton("Entfernen", (d, w) -> { routes.remove(position); rebuildRoutes(); }).show());
            remove.setEnabled(routes.length() > 1);
        }
        if (!triggerRouteIDs.contains(triggerRouteID) && !triggerRouteIDs.isEmpty()) triggerRouteID = triggerRouteIDs.get(0);
        setChoices(triggerRoute, names.toArray(new String[0]), Math.max(0, triggerRouteIDs.indexOf(triggerRouteID)));
        triggerRoute.setOnItemSelectedListener(selected(() -> {
            int index = triggerRoute.getSelectedItemPosition(); if (index >= 0 && index < triggerRouteIDs.size()) triggerRouteID = triggerRouteIDs.get(index);
        })); triggerRoute.setEnabled(trigger.getSelectedItemPosition() == 0);
    }

    private void editRoute(int position) {
        if (position < 0 && routes.length() >= 32) { toast("Maximal 32 Rufrouten möglich"); return; }
        JSONObject original = position < 0 ? new JSONObject() : routes.optJSONObject(position);
        LinearLayout form = group(); form.setPadding(dp(20), dp(8), dp(20), dp(8)); Map<String, EditText> edits = new LinkedHashMap<>();
        String[][] specs = {{"name", "Name"}, {"id", "Kennung (Kleinbuchstaben, Ziffern, _)"}, {"doorbell_number", "Rufziel des Türkontos"},
                {"mobile_number_1", "Parallelruf-Ziel 1"}, {"mobile_number_2", "Parallelruf-Ziel 2"}, {"mobile_number_3", "Parallelruf-Ziel 3"}};
        String newID = "route_" + (routes.length() + 1);
        for (int suffix = routes.length() + 2; triggerRouteIDs.contains(newID); suffix++) newID = "route_" + suffix;
        for (String[] spec : specs) {
            note(form, spec[1]); EditText e = input(spec[1], false); e.setText(original.optString(spec[0], spec[0].equals("id") ? newID : "")); form.addView(e); edits.put(spec[0], e);
        }
        ScrollView container = new ScrollView(this); container.addView(form);
        AlertDialog dialog = new AlertDialog.Builder(this).setTitle(position < 0 ? "Neue Rufroute" : "Rufroute bearbeiten")
                .setView(container).setNegativeButton("Abbrechen", null).setPositiveButton("Übernehmen", null).create();
        dialog.setOnShowListener(d -> dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(v -> {
            try {
                JSONObject route = new JSONObject(original.toString());
                for (Map.Entry<String, EditText> entry : edits.entrySet()) route.put(entry.getKey(), entry.getValue().getText().toString().trim());
                String id = route.getString("id");
                if (!id.matches("[a-z][a-z0-9_]{0,63}")) throw new IllegalArgumentException("Kennung: mit Kleinbuchstaben beginnen, höchstens 64 Zeichen");
                if (route.getString("name").isEmpty()) throw new IllegalArgumentException("Bitte einen Namen eingeben");
                for (int i = 0; i < routes.length(); i++) if (i != position && id.equals(routes.getJSONObject(i).getString("id"))) throw new IllegalArgumentException("Diese Kennung ist bereits vergeben");
                if (position >= 0 && original.optString("id").equals(triggerRouteID)) triggerRouteID = id;
                if (!route.has("visitor_entity")) route.put("visitor_entity", "");
                if (position < 0) routes.put(route); else routes.put(position, route);
                rebuildRoutes(); dialog.dismiss(); toast("Route übernommen. Einstellungen noch speichern.");
            } catch (Exception e) { toast(e.getMessage()); }
        })); dialog.show();
    }

    private void exportConfig() {
        new AlertDialog.Builder(this).setTitle("Konfiguration exportieren").setMessage("Die Datei enthält die gespeicherten Kamera- und SIP-Passwörter im Klartext. An einem geschützten Ort speichern.")
                .setNegativeButton("Abbrechen", null).setPositiveButton("Datei speichern", (d, w) -> executor.execute(() -> {
                    try {
                        String export = new JSONObject(Mobilebridge.normalizeConfig(ConfigStore.buildJson(this))).toString(2);
                        runOnUiThread(() -> {
                            if (isDestroyed()) return; pendingExport = export;
                            try { startActivityForResult(new Intent(Intent.ACTION_CREATE_DOCUMENT).setType("application/json").addCategory(Intent.CATEGORY_OPENABLE)
                                    .putExtra(Intent.EXTRA_TITLE, "reolink-gateway-" + DateFormat.format("yyyy-MM-dd", System.currentTimeMillis()) + ".json"), EXPORT); }
                            catch (Exception e) { pendingExport = null; toast("Dateiauswahl nicht verfügbar"); }
                        });
                    } catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
                })).show();
    }

    @Override protected void onActivityResult(int request, int result, Intent data) {
        super.onActivityResult(request, result, data);
        if (request != EXPORT && request != IMPORT) return;
        if (result != RESULT_OK || data == null || data.getData() == null) { if (request == EXPORT) pendingExport = null; return; }
        Uri uri = data.getData(); String export = pendingExport; pendingExport = null; JSONObject local = ConfigStore.read(this);
        executor.execute(() -> {
            try {
                if (request == EXPORT) {
                    if (export == null) throw new IllegalStateException("Export bitte erneut starten");
                    try (OutputStream output = getContentResolver().openOutputStream(uri, "wt")) {
                        if (output == null) throw new IllegalStateException("Datei konnte nicht geöffnet werden"); output.write(export.getBytes(StandardCharsets.UTF_8));
                    }
                    runOnUiThread(() -> toast("Konfiguration exportiert"));
                } else {
                    String raw; try (InputStream input = getContentResolver().openInputStream(uri)) { raw = readText(input, MAX_IMPORT_BYTES); }
                    JSONObject imported = ConfigStore.fromJson(Mobilebridge.normalizeConfig(raw), local);
                    runOnUiThread(() -> {
                        if (isDestroyed()) return; values = imported; selectedPage = 1; render(); toast("Konfiguration geprüft und geladen. Mit Speichern übernehmen.");
                    });
                }
            } catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
        });
    }

    private static String readText(InputStream input, int limit) throws Exception {
        if (input == null) throw new IllegalStateException("Datei konnte nicht geöffnet werden");
        ByteArrayOutputStream buffer = new ByteArrayOutputStream(); byte[] block = new byte[4096]; int count;
        while ((count = input.read(block)) != -1) {
            if (buffer.size() + count > limit) throw new IllegalArgumentException("Datei ist zu groß"); buffer.write(block, 0, count);
        }
        return new String(buffer.toByteArray(), StandardCharsets.UTF_8);
    }

    private void loadImage() {
        if (!resumed || selectedPage != 0 || !previewRequested || imageBusy) return;
        imageBusy = true; imageStatus.setText("Bild wird geladen …");
        imageExecutor.execute(() -> {
            Bitmap bitmap = null; String failure = "";
            try {
                byte[] jpeg = GatewayService.snapshotJPEG(); bitmap = BitmapFactory.decodeByteArray(jpeg, 0, jpeg.length);
                if (bitmap == null) throw new IllegalStateException("Kamerabild konnte nicht geöffnet werden");
            } catch (Exception e) { failure = e.getMessage(); }
            final Bitmap result = bitmap; final String error = failure;
            runOnUiThread(() -> {
                imageBusy = false; if (isDestroyed() || !resumed || selectedPage != 0 || !previewRequested) return;
                image.setImageBitmap(result);
                imageStatus.setText(result == null ? ConfigStore.redact(this, error == null ? "Bildabruf fehlgeschlagen" : error) : "Kamerabild · " + DateFormat.format("HH:mm:ss", System.currentTimeMillis()));
                handler.removeCallbacks(imageRefresh); handler.postDelayed(imageRefresh, result == null ? 8000 : 3000);
            });
        });
    }

    @Override protected void onResume() { super.onResume(); resumed = true; handler.post(refresh); handler.post(imageRefresh); }
    @Override protected void onPause() { resumed = false; handler.removeCallbacks(refresh); handler.removeCallbacks(imageRefresh); super.onPause(); }
    @Override protected void onDestroy() { executor.shutdown(); imageExecutor.shutdown(); super.onDestroy(); }
    @Override protected void onSaveInstanceState(Bundle out) {
        try { out.putString("form", collect(false).toString()); } catch (Exception ignored) {}
        out.putInt("page", selectedPage); out.putString("export", pendingExport); super.onSaveInstanceState(out);
    }
    private void runAsync(Action action) {
        if (actionBusy) return; actionBusy = true; refreshControls();
        executor.execute(() -> {
            try { action.run(); runOnUiThread(() -> toast("Anforderung ausgeführt")); }
            catch (Exception e) { runOnUiThread(() -> toast(e.getMessage())); }
            finally { runOnUiThread(() -> { actionBusy = false; if (!isDestroyed()) refreshControls(); }); }
        });
    }
    private void clipboard(String name, String value, boolean sensitive) {
        ClipData clip = ClipData.newPlainText(name, value);
        if (sensitive && Build.VERSION.SDK_INT >= 33) { PersistableBundle extras = new PersistableBundle(); extras.putBoolean("android.content.extra.IS_SENSITIVE", true); clip.getDescription().setExtras(extras); }
        getSystemService(ClipboardManager.class).setPrimaryClip(clip);
    }
    private LinearLayout group() { LinearLayout l = new LinearLayout(this); l.setOrientation(LinearLayout.VERTICAL); return l; }
    private LinearLayout card(LinearLayout root) {
        LinearLayout card = group(); card.setPadding(dp(16), dp(12), dp(16), dp(16));
        GradientDrawable bg = new GradientDrawable(); bg.setColor(Color.WHITE); bg.setCornerRadius(dp(16)); bg.setStroke(dp(1), 0xffdce6e1); card.setBackground(bg);
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(-1, -2); params.topMargin = dp(12); root.addView(card, params); return card;
    }
    private LinearLayout expandable(LinearLayout root, String title, String subtitle, boolean expanded) {
        LinearLayout card = card(root), child = group(); child.setVisibility(expanded ? View.VISIBLE : View.GONE);
        Button heading = button(card, title + (expanded ? " ▴" : " ▾"), null); heading.setGravity(Gravity.START | Gravity.CENTER_VERTICAL);
        heading.setOnClickListener(v -> { boolean show = child.getVisibility() == View.GONE; child.setVisibility(show ? View.VISIBLE : View.GONE); heading.setText(title + (show ? " ▴" : " ▾")); });
        note(card, subtitle); card.addView(child); return child;
    }
    private TextView label(LinearLayout root, String text, int size, int color) {
        TextView v = new TextView(this); v.setText(text); v.setTextSize(size); v.setTextColor(color); v.setPadding(0, dp(4), 0, dp(8)); root.addView(v); return v;
    }
    private void section(LinearLayout root, String text) { label(root, text, 18, INK).setTypeface(null, Typeface.BOLD); }
    private void note(LinearLayout root, String text) { label(root, text, 14, MUTED); }
    private EditText input(String label, boolean secret) {
        EditText e = new EditText(this); e.setSingleLine(true); e.setContentDescription(label); e.setTextSize(16);
        e.setInputType(InputType.TYPE_CLASS_TEXT | (secret ? InputType.TYPE_TEXT_VARIATION_PASSWORD : InputType.TYPE_TEXT_FLAG_NO_SUGGESTIONS)); return e;
    }
    private EditText field(LinearLayout root, String key, String label, String def, boolean secret) {
        note(root, label); EditText e = input(label, secret); e.setText(values.optString(key, def)); root.addView(e); fields.put(key, e); return e;
    }
    private void number(LinearLayout root, String key, String label, int def, int min, int max) {
        field(root, key, label, Integer.toString(def), false).setInputType(InputType.TYPE_CLASS_NUMBER); ranges.put(key, new int[]{min, max});
    }
    private void check(LinearLayout root, String key, String label, boolean def) {
        CheckBox c = new CheckBox(this); c.setText(label); c.setChecked(values.optBoolean(key, def)); c.setMinHeight(dp(48)); root.addView(c); checks.put(key, c);
    }
    private Spinner spinner(LinearLayout root, String[] choices, int selected) {
        Spinner s = new Spinner(this); s.setMinimumHeight(dp(48)); setChoices(s, choices, selected); root.addView(s); return s;
    }
    private void setChoices(Spinner s, String[] choices, int selected) {
        ArrayAdapter<String> adapter = new ArrayAdapter<>(this, android.R.layout.simple_spinner_item, choices);
        adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item); s.setAdapter(adapter); if (choices.length > 0) s.setSelection(Math.min(selected, choices.length - 1));
    }
    private AdapterView.OnItemSelectedListener selected(Runnable action) {
        return new AdapterView.OnItemSelectedListener() {
            public void onItemSelected(AdapterView<?> p, View v, int position, long id) { action.run(); }
            public void onNothingSelected(AdapterView<?> p) {}
        };
    }
    private Button button(LinearLayout root, String label, View.OnClickListener listener) {
        Button b = new Button(this); b.setText(label); b.setAllCaps(false); b.setMinHeight(dp(48)); b.setOnClickListener(listener); root.addView(b); return b;
    }
    private Button rowButton(LinearLayout row, String label, View.OnClickListener listener) {
        Button b = new Button(this); b.setText(label); b.setAllCaps(false); b.setMinWidth(0); b.setMinimumWidth(0); b.setOnClickListener(listener); row.addView(b, new LinearLayout.LayoutParams(0, -2, 1)); return b;
    }
    private static int index(String[] choices, String value) { for (int i = 0; i < choices.length; i++) if (choices[i].equals(value)) return i; return 0; }
    private void toast(String message) { if (!isDestroyed()) Toast.makeText(this, message == null ? "Unbekannter Fehler" : ConfigStore.redact(this, message), Toast.LENGTH_LONG).show(); }
    private int dp(int n) { return Math.round(n * getResources().getDisplayMetrics().density); }
    private interface Action { void run() throws Exception; }
}
