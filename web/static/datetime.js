// GatusMOD clock and date notation for the Gatus dashboard. Gatus formats its timestamps with the toLocale*String
// methods of Date, so they are replaced before the Gatus scripts run. Settings come from window.gatusmod.
(function () {
  var settings = window.gatusmod || {};
  var timeFormat = settings.timeFormat || '';
  var dateFormat = settings.dateFormat || '';
  if (!timeFormat && !dateFormat) {
    return;
  }
  var proto = Date.prototype;
  var original = {
    string: proto.toLocaleString,
    date: proto.toLocaleDateString,
    time: proto.toLocaleTimeString
  };

  function pad(n) {
    return (n < 10 ? '0' : '') + n;
  }

  function datePart(d) {
    if (!dateFormat) {
      return original.date.call(d);
    }
    var day = pad(d.getDate()), month = pad(d.getMonth() + 1), year = d.getFullYear();
    return dateFormat === 'MM/DD/YYYY' ? month + '/' + day + '/' + year : day + '/' + month + '/' + year;
  }

  function timePart(d) {
    if (!timeFormat) {
      return original.time.call(d);
    }
    var h = d.getHours(), rest = ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
    if (timeFormat === '24h') {
      return pad(h) + rest;
    }
    return (h % 12 || 12) + rest + (h < 12 ? ' AM' : ' PM');
  }

  // Calls with explicit options keep them, except that a shown hour follows the chosen clock. hourCycle is used
  // instead of hour12, because hour12:false renders midnight as 24:00 in some browsers.
  function withClock(options) {
    if (!timeFormat || !options || options.hour === undefined) {
      return options;
    }
    var copy = {};
    for (var k in options) {
      if (Object.prototype.hasOwnProperty.call(options, k) && k !== 'hour12') {
        copy[k] = options[k];
      }
    }
    copy.hourCycle = timeFormat === '24h' ? 'h23' : 'h12';
    return copy;
  }

  function replace(name, format) {
    var fn = original[name];
    return function (locales, options) {
      if (isNaN(this.getTime())) {
        return fn.apply(this, arguments);
      }
      if (locales === undefined && options === undefined) {
        return format(this);
      }
      return fn.call(this, locales, withClock(options));
    };
  }

  proto.toLocaleString = replace('string', function (d) { return datePart(d) + ', ' + timePart(d); });
  proto.toLocaleDateString = replace('date', datePart);
  proto.toLocaleTimeString = replace('time', timePart);
})();
