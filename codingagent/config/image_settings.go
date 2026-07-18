package config

func (manager *SettingsManager) GetShowImages() bool {
	return boolDefault(manager.objectValue("terminal"), "showImages", true)
}

func (manager *SettingsManager) GetImageWidthCells() int {
	width := optionalInt64(manager.objectValue("terminal"), "imageWidthCells")
	if width == nil {
		return 60
	}
	return max(1, int(*width))
}

func (manager *SettingsManager) GetImageAutoResize() bool {
	return boolDefault(manager.objectValue("images"), "autoResize", true)
}
