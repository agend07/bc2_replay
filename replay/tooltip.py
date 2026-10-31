"""r286: hover help for panel controls (a small yellow box under the widget after a short delay)."""
import tkinter as tk

DELAY_MS = 500
WRAP_PX = 360


def tip(widget, text):
    """Show text while the mouse rests on widget. Returns widget (chainable)."""
    state = {'job': None, 'win': None}

    def show():
        state['job'] = None
        if state['win'] is not None:
            return
        win = tk.Toplevel(widget)
        win.wm_overrideredirect(True)
        try:
            win.wm_attributes('-topmost', True)
        except tk.TclError:
            pass
        tk.Label(win, text=text, justify='left', wraplength=WRAP_PX, background='#ffffe0', foreground='#000000',
                 relief='solid', borderwidth=1, padx=6, pady=4).pack()
        win.update_idletasks()
        x = widget.winfo_rootx()
        y = widget.winfo_rooty() + widget.winfo_height() + 4
        x = max(0, min(x, widget.winfo_screenwidth() - win.winfo_reqwidth() - 4))
        if y + win.winfo_reqheight() > widget.winfo_screenheight():
            y = widget.winfo_rooty() - win.winfo_reqheight() - 4
        win.wm_geometry(f'+{x}+{y}')
        state['win'] = win

    def enter(_e=None):
        if state['job'] is None and state['win'] is None:
            state['job'] = widget.after(DELAY_MS, show)

    def leave(_e=None):
        if state['job'] is not None:
            widget.after_cancel(state['job'])
            state['job'] = None
        if state['win'] is not None:
            state['win'].destroy()
            state['win'] = None

    widget.bind('<Enter>', enter, add='+')
    widget.bind('<Leave>', leave, add='+')
    widget.bind('<ButtonPress>', leave, add='+')
    widget.bind('<Destroy>', leave, add='+')
    return widget
