# Loxone

The relay gives Loxone the same Virtual HTTP Input template and values as
Ciconia Cloud.

1. In the panel, open **Integrations** and click **Download template**.
2. In Loxone Config, import `VI_Ciconia.xml`: **Virtual Inputs → Virtual HTTP
   Input Templates → Import Template…**
3. Add **Ciconia** from the same menu and save to the Miniserver.

The inputs are described in the
[Ciconia docs](https://getciconia.com/docs/loxone/inputs/).

When the panel says **Template out of date** (after you add, rename or remove
a tracker or an area), download and import it again.

The Miniserver reads the relay on port `8080`, so it must reach it on your
home network. As a Home Assistant app, publish port `8080` under the app's
**Network** settings; the template is then also at `<panel>/loxone/template`.
If the Miniserver reaches the relay at a different address than your browser
does, set `RELAY_LOXONE_URL`.
