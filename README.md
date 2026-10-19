# Client Launcher

Launcher Minecraft (Fabric) multi-packs avec mises à jour automatiques.

- Connexion Microsoft, téléchargement automatique de Minecraft, Fabric et Java.
- Plusieurs packs, chacun avec son dossier de jeu.
- Bouton Serveur / Solo (connexion directe à `minecraft.nexusdescartes.com`).
- Mises à jour automatiques signées, publiées dans les **Releases** de ce dépôt.

## Mises à jour

Les launchers lisent la dernière release :
`https://github.com/Mpixel44/client-launcher/releases/latest/download/manifest.json`

Chaque release contient `manifest.json`, `manifest.sig` et les fichiers nommés par leur empreinte SHA-256.
Elle est préparée par le launcher de l'administrateur (Paramètres > Mises à jour > Préparer).

## Compiler

```
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o Launcher.exe .
```
