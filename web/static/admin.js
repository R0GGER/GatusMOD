document.addEventListener('click', function (event) {
  var button = event.target.closest('[data-copy]');
  if (!button) {
    return;
  }
  var input = button.parentElement.querySelector('input');
  navigator.clipboard.writeText(input.value).then(function () {
    var label = button.textContent;
    button.textContent = 'Copied';
    setTimeout(function () { button.textContent = label; }, 1200);
  }, function () {
    input.select();
  });
});

document.addEventListener('submit', function (event) {
  var message = event.target.getAttribute('data-confirm');
  if (message && !window.confirm(message)) {
    event.preventDefault();
    return;
  }
  var submitter = event.submitter;
  var busy = submitter && submitter.getAttribute('data-busy-label');
  if (busy) {
    // Disabling the button right away would drop its name and value from the submitted form.
    setTimeout(function () {
      submitter.textContent = busy;
      submitter.disabled = true;
    }, 0);
  }
});

(function () {
  var search = document.getElementById('badge-search');
  if (!search) {
    return;
  }
  var cards = document.querySelectorAll('.badge-card[data-search]');
  var status = document.getElementById('badge-search-status');
  var maxResults = 20;

  function apply() {
    var terms = search.value.toLowerCase().split(/\s+/).filter(Boolean);
    var matches = 0;
    cards.forEach(function (card) {
      var text = card.getAttribute('data-search').toLowerCase();
      var match = terms.length > 0 && terms.every(function (t) { return text.indexOf(t) !== -1; });
      if (match) {
        matches++;
      }
      card.hidden = !match || matches > maxResults;
    });
    if (terms.length === 0) {
      status.textContent = cards.length + ' endpoints. Type to search.';
    } else if (matches === 0) {
      status.textContent = 'No endpoints match.';
    } else if (matches > maxResults) {
      status.textContent = 'Showing ' + maxResults + ' of ' + matches + ' matches. Refine your search.';
    } else {
      status.textContent = matches + (matches === 1 ? ' match' : ' matches');
    }

    var url = new URL(window.location.href);
    if (search.value) {
      url.searchParams.set('q', search.value);
    } else {
      url.searchParams.delete('q');
    }
    history.replaceState(null, '', url);
    document.querySelectorAll('a[data-keep-query]').forEach(function (link) {
      var href = new URL(link.href);
      if (search.value) {
        href.searchParams.set('q', search.value);
      } else {
        href.searchParams.delete('q');
      }
      link.href = href;
    });
    document.querySelectorAll('input[data-keep-query]').forEach(function (input) {
      input.value = search.value;
      input.disabled = !search.value;
    });
  }

  search.value = new URL(window.location.href).searchParams.get('q') || '';
  search.addEventListener('input', apply);
  apply();
  if (!search.value) {
    search.focus();
  }
})();

(function () {
  var search = document.getElementById('check-search');
  var state = document.getElementById('check-state');
  if (!search || !state) {
    return;
  }
  var rows = document.querySelectorAll('[data-check-table] tr[data-state]');
  var status = document.getElementById('check-status');

  function matchesState(row) {
    var s = row.getAttribute('data-state');
    if (state.value === 'problem') {
      return s === 'down' || s === 'pending';
    }
    return !state.value || s === state.value;
  }

  function apply() {
    var terms = search.value.toLowerCase().split(/\s+/).filter(Boolean);
    var shown = 0;
    rows.forEach(function (row) {
      var text = row.getAttribute('data-search').toLowerCase();
      var match = matchesState(row) && terms.every(function (t) { return text.indexOf(t) !== -1; });
      row.hidden = !match;
      if (match) {
        shown++;
      }
    });
    status.textContent = shown === rows.length ? rows.length + ' rows' : 'Showing ' + shown + ' of ' + rows.length + ' rows';

    var url = new URL(window.location.href);
    [['q', search.value], ['state', state.value]].forEach(function (p) {
      if (p[1]) {
        url.searchParams.set(p[0], p[1]);
      } else {
        url.searchParams.delete(p[0]);
      }
    });
    history.replaceState(null, '', url);
  }

  var params = new URL(window.location.href).searchParams;
  search.value = params.get('q') || '';
  state.value = params.get('state') || '';
  search.addEventListener('input', apply);
  state.addEventListener('change', apply);
  apply();
})();

(function () {
  var form = document.querySelector('form[data-table-form]');
  if (!form) {
    return;
  }
  var tbody = document.getElementById('rows');
  var template = document.getElementById('row-template');
  var next = parseInt(tbody.getAttribute('data-next'), 10) || 0;

  function renumber(row) {
    var index = next++;
    row.querySelectorAll('[name]').forEach(function (field) {
      field.name = field.name.replace(/^row\.[^.]+\./, 'row.' + index + '.');
    });
    return row;
  }

  function hasValue(field) {
    if (field.type === 'hidden') {
      return false;
    }
    if (field.type === 'checkbox') {
      return field.checked !== field.hasAttribute('data-default-on');
    }
    return field.value.trim() !== '';
  }

  // Empty Group cells show the table group they inherit; cells that held the old table group follow a change of it.
  var tableGroup = form.querySelector('[data-table-group]');
  var lastGroup = tableGroup ? tableGroup.value.trim() : '';
  function groupCells() {
    return tbody.querySelectorAll('td[data-col="group"] input');
  }
  function showGroup() {
    groupCells().forEach(function (cell) {
      cell.placeholder = lastGroup ? lastGroup + ' (table group)' : '';
    });
  }
  if (tableGroup) {
    tableGroup.addEventListener('input', function () {
      var group = tableGroup.value.trim();
      groupCells().forEach(function (cell) {
        if (lastGroup && cell.value.trim() === lastGroup) {
          cell.value = group;
        }
      });
      lastGroup = group;
      showGroup();
    });
    showGroup();
  }

  form.addEventListener('click', function (event) {
    var button = event.target.closest('button');
    if (!button) {
      return;
    }
    if (button.hasAttribute('data-add-row')) {
      var row = renumber(template.content.firstElementChild.cloneNode(true));
      tbody.appendChild(row);
      showGroup();
      var first = row.querySelector('td:not([hidden]) input, td:not([hidden]) textarea');
      if (first) {
        first.focus();
      }
    } else if (button.hasAttribute('data-row-copy')) {
      var source = button.closest('tr');
      var copy = source.cloneNode(true);
      var from = source.querySelectorAll('[name]');
      copy.querySelectorAll('[name]').forEach(function (field, i) {
        if (field.type === 'checkbox') {
          field.checked = from[i].checked;
        } else {
          field.value = from[i].value;
        }
      });
      copy.querySelectorAll('.cell-error').forEach(function (el) { el.remove(); });
      copy.querySelectorAll('td.invalid').forEach(function (el) { el.classList.remove('invalid'); });
      source.after(renumber(copy));
    } else if (button.hasAttribute('data-row-delete')) {
      button.closest('tr').remove();
    }
  });

  form.addEventListener('change', function (event) {
    var key = event.target.getAttribute('data-col-toggle');
    if (!key) {
      return;
    }
    form.querySelectorAll('[data-col="' + key + '"]').forEach(function (el) {
      el.hidden = !event.target.checked;
    });
  });

  form.addEventListener('submit', function (event) {
    var dropped = [];
    form.querySelectorAll('[data-col-toggle]').forEach(function (toggle) {
      if (toggle.checked || toggle.disabled) {
        return;
      }
      var key = toggle.getAttribute('data-col-toggle');
      var used = Array.prototype.some.call(tbody.querySelectorAll('td[data-col="' + key + '"] [name]'), hasValue);
      if (used) {
        dropped.push(toggle.parentElement.textContent.trim());
      }
    });
    if (dropped.length && !window.confirm('These hidden columns still have values that will be removed: ' + dropped.join(', ') + '. Save anyway?')) {
      event.preventDefault();
    }
  });
})();

// Import form: the URL template examples and default placeholders follow the primary check.
(function () {
  var select = document.querySelector('[data-type-select]');
  var input = document.getElementById('urlTemplate');
  if (!select || !input) {
    return;
  }
  var conditions = document.getElementById('conditions');
  function apply() {
    document.querySelectorAll('[data-type-hint]').forEach(function (hint) {
      var current = hint.getAttribute('data-type-hint') === select.value;
      hint.hidden = !current;
      if (!current) {
        return;
      }
      var url = hint.getAttribute('data-default-template');
      input.placeholder = url ? 'default: ' + url : 'required: a URL column or a template';
      if (conditions) {
        var conds = hint.getAttribute('data-default-conditions');
        conditions.placeholder = conds ? 'default: ' + conds.replace(/\n/g, ', ') : 'none';
      }
    });
  }
  select.addEventListener('change', apply);
  document.addEventListener('click', function (event) {
    var button = event.target.closest('[data-example-template]');
    if (!button) {
      return;
    }
    input.value = button.getAttribute('data-example-template');
    input.focus();
  });
})();

document.addEventListener('change', function (event) {
  var target = event.target;
  if (target.hasAttribute('data-autosubmit')) {
    target.form.submit();
  }
  var toggle = target.getAttribute('data-toggle');
  if (toggle) {
    var input = document.getElementById(toggle);
    input.disabled = !target.checked;
    if (target.checked) {
      input.focus();
    }
  }
});
