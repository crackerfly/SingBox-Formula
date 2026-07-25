'use strict';

(function() {
	var script = document.currentScript ||
		document.querySelector('script[data-liquid-formula-logo][data-liquid-formula-favicon]');
	var logo = script && script.getAttribute('data-liquid-formula-logo');
	var favicon = script && script.getAttribute('data-liquid-formula-favicon');

	if (favicon) {
		var icons = document.querySelectorAll('link[rel~="icon"]');
		if (!icons.length) {
			var icon = document.createElement('link');
			icon.rel = 'icon';
			document.head.appendChild(icon);
			icons = [icon];
		}

		for (var i = 0; i < icons.length; i++) {
			icons[i].href = favicon;
			icons[i].removeAttribute('sizes');
			icons[i].removeAttribute('type');
		}
	}

	if (!logo)
		return;

	var selectors = [
		'.login-form > a.brand > img.icon',
		'.login-form a.brand > img.icon',
		'header.header .header__title.brand > img.icon',
		'.ms-card-header > a.brand > img.icon'
	];
	var brandImages = document.querySelectorAll(selectors.join(','));

	for (var j = 0; j < brandImages.length; j++) {
		brandImages[j].src = logo;
		brandImages[j].removeAttribute('srcset');
	}
})();
